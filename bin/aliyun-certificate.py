#!/usr/bin/env python3
"""Certbot DNS-01 and Aliyun CDN hooks. Requires only Python 3 and openssl."""
import argparse
import base64
import datetime
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import subprocess
import socket
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def quote(value):
    return urllib.parse.quote(str(value), safe='~')


def signed_params(params, key_id, secret):
    params = dict(params, AccessKeyId=key_id, Format='JSON', SignatureMethod='HMAC-SHA1',
                  SignatureVersion='1.0', SignatureNonce=str(uuid.uuid4()),
                  Timestamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))
    canonical = '&'.join(quote(k) + '=' + quote(v) for k, v in sorted(params.items()))
    message = 'POST&%2F&' + quote(canonical)
    params['Signature'] = base64.b64encode(hmac.new((secret + '&').encode(), message.encode(), hashlib.sha1).digest()).decode()
    return params


class API:
    def __init__(self, config):
        self.key = Path(config['access_key_id_file']).read_text().strip()
        self.secret = Path(config['access_key_secret_file']).read_text().strip()
        if not self.key or not self.secret:
            raise ValueError('AccessKey files are empty')

    def call(self, service, action, **params):
        version = {'alidns': '2015-01-09', 'cdn': '2018-05-10'}[service]
        data = urllib.parse.urlencode(signed_params(dict(params, Action=action, Version=version), self.key, self.secret)).encode()
        request = urllib.request.Request('https://' + service + '.aliyuncs.com/', data=data)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                result = json.load(response)
        except urllib.error.HTTPError as error:
            try:
                result = json.load(error)
            except (ValueError, OSError):
                raise RuntimeError('Aliyun API HTTP ' + str(error.code)) from None
        except (urllib.error.URLError, OSError):
            raise RuntimeError('Aliyun API connection failed') from None
        if 'Code' in result:
            # Never expose a response Message: providers may echo request fields.
            code = re.sub(r'[^a-zA-Z0-9_.-]', '', str(result['Code']))[:100]
            request_id = re.sub(r'[^a-zA-Z0-9-]', '', str(result.get('RequestId', '')))[:100]
            raise RuntimeError(action + ': ' + code + ' RequestId=' + request_id)
        return result


def validate_config(config):
    for name in ('domain', 'dns_zone'):
        if not re.fullmatch(r'[a-z0-9]+(?:[a-z0-9.-]*[a-z0-9])?', config[name]):
            raise ValueError('Invalid domain configuration')
    domain, zone = config['domain'], config['dns_zone']
    if domain != zone and not domain.endswith('.' + zone):
        raise ValueError('Certificate domain is outside DNS zone')
    delay = int(config.get('propagation_seconds', 120))
    if delay < 0 or delay > 600:
        raise ValueError('Invalid DNS propagation delay')


def challenge(config):
    if os.environ.get('CERTBOT_DOMAIN') != config['domain']:
        raise ValueError('Certbot domain does not match configured domain')
    value = os.environ.get('CERTBOT_VALIDATION', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{20,200}', value):
        raise ValueError('Invalid Certbot validation value')
    full = '_acme-challenge.' + config['domain']
    rr = full[:-(len(config['dns_zone']) + 1)]
    return rr, value


def records(api, config, rr):
    found = []
    page = 1
    while True:
        response = api.call('alidns', 'DescribeDomainRecords', DomainName=config['dns_zone'],
                            RRKeyWord=rr, TypeKeyWord='TXT', PageNumber=page, PageSize=100)
        rows = response.get('DomainRecords', {}).get('Record', [])
        found.extend(r for r in rows if r.get('RR') == rr and r.get('Type') == 'TXT')
        if page * 100 >= int(response.get('TotalCount', len(rows))):
            return found
        page += 1


def auth(api, config):
    rr, value = challenge(config)
    # An identical existing token is not ours to delete.
    if any(r.get('Value') == value for r in records(api, config, rr)):
        raise ValueError('Identical challenge TXT already exists; refusing to claim ownership')
    response = api.call('alidns', 'AddDomainRecord', DomainName=config['dns_zone'], RR=rr, Type='TXT', Value=value, TTL=600)
    record_id = str(response.get('RecordId', ''))
    if not re.fullmatch(r'[0-9]+', record_id):
        raise RuntimeError('DNS API did not return a valid record ID')
    print(record_id, flush=True)
    print('Challenge TXT created; waiting for DNS propagation', file=sys.stderr)
    time.sleep(int(config.get('propagation_seconds', 120)))


def cleanup(api, config):
    rr, value = challenge(config)
    record_id = os.environ.get('CERTBOT_AUTH_OUTPUT', '').strip()
    if not re.fullmatch(r'[0-9]+', record_id):
        raise ValueError('Missing or invalid auth record ID; refusing cleanup')
    matches = [r for r in records(api, config, rr) if str(r.get('RecordId')) == record_id]
    if not matches:
        return
    if (len(matches) != 1 or matches[0].get('Value') != value
            or matches[0].get('DomainName', config['dns_zone']) != config['dns_zone']):
        raise ValueError('Challenge record no longer matches; refusing cleanup')
    api.call('alidns', 'DeleteDomainRecord', RecordId=record_id)
    print('Challenge TXT removed', file=sys.stderr)


def deployed_matches(domain, pem):
    leaf = re.search(r'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', pem, re.S)
    if not leaf:
        return False
    expected = ssl.PEM_cert_to_DER_cert(leaf.group())
    try:
        with socket.create_connection((domain, 443), timeout=15) as connection:
            with ssl.create_default_context().wrap_socket(connection, server_hostname=domain) as tls:
                return tls.getpeercert(binary_form=True) == expected
    except (OSError, ValueError):
        return False


def deploy(api, config):
    lineage = os.environ.get('RENEWED_LINEAGE', '')
    if not lineage or not Path(lineage).is_absolute():
        raise ValueError('RENEWED_LINEAGE must be an absolute path')
    domains = os.environ.get('RENEWED_DOMAINS', '').split()
    if domains and domains != [config['domain']]:
        raise ValueError('Renewed domains do not match configured domain')
    cert = Path(lineage) / 'fullchain.pem'
    key = Path(lineage) / 'privkey.pem'
    def openssl(*args):
        result = subprocess.run(['openssl', *args], capture_output=True)
        if result.returncode:
            raise ValueError('Certificate validation failed')
        return result.stdout
    openssl('x509', '-in', str(cert), '-noout', '-checkend', '0')
    san = openssl('x509', '-in', str(cert), '-noout', '-ext', 'subjectAltName').decode()
    if re.findall(r'DNS:([^,\s]+)', san) != [config['domain']]:
        raise ValueError('Certificate SAN does not match configured domain')
    cert_pub = openssl('x509', '-in', str(cert), '-pubkey', '-noout')
    key_pub = openssl('pkey', '-in', str(key), '-pubout')
    if cert_pub != key_pub:
        raise ValueError('Certificate and private key do not match')
    # Daily retry timer must never upload a staging certificate.
    issuer = openssl('x509', '-in', str(cert), '-noout', '-issuer').decode().lower()
    if 'staging' in issuer or 'fake' in issuer:
        raise ValueError('Refusing to deploy a staging certificate')
    if deployed_matches(config['domain'], cert.read_text()):
        print('CDN already serves the current valid certificate')
        return
    api.call('cdn', 'SetCdnDomainSSLCertificate', DomainName=config['domain'], SSLProtocol='on',
             CertType='upload', CertName='stoneage-' + config['domain'] + '-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S') + '-' + uuid.uuid4().hex[:8],
             SSLPub=cert.read_text(), SSLPri=key.read_text())
    print('Certificate deployed to ' + config['domain'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['check', 'auth', 'cleanup', 'deploy'])
    parser.add_argument('--config', default=os.environ.get('STONEAGE_CERT_CONFIG', '/opt/stoneage/config/certificates/aliyun.json'))
    args = parser.parse_args()
    config = json.loads(Path(args.config).read_text())
    validate_config(config)
    api = API(config)
    if args.command == 'check':
        api.call('alidns', 'DescribeDomainRecords', DomainName=config['dns_zone'], PageSize=1)
        api.call('cdn', 'DescribeCdnDomainDetail', DomainName=config['domain'])
        print('DNS and CDN read permissions verified')
    else:
        globals()[args.command](api, config)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, RuntimeError) as error:
        # API errors are sanitized above; never dump requests or tracebacks.
        print('Certificate hook failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
