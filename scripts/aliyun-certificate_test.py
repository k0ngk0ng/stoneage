#!/usr/bin/env python3
"""Offline tests for the current Aliyun certificate hook implementation."""

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent.parent
BUILD = ROOT / "build"
BUILD.mkdir(exist_ok=True)
SPEC = importlib.util.spec_from_file_location(
    "stoneage_aliyun_certificate", ROOT / "bin/aliyun-certificate.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class FakeAPI:
    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def call(self, service, action, **params):
        self.calls.append((service, action, params))
        response = self.responses.get(action)
        if callable(response):
            return response(service, action, params)
        if response is None:
            raise AssertionError(f"unexpected API action: {service}/{action}")
        return response


class CertificateHookTests(unittest.TestCase):
    def setUp(self) -> None:
        deployed_patch = mock.patch.object(MODULE, "deployed_matches", return_value=False)
        deployed_patch.start()
        self.addCleanup(deployed_patch.stop)
        # Keep every temporary file inside the shared workspace.  This also
        # ensures the tests honor the repository's filesystem restriction.
        self.temp = tempfile.TemporaryDirectory(dir=BUILD, prefix="aliyun-cert-test-")
        self.root = Path(self.temp.name)
        self.key_id = self.root / "access-key-id"
        self.key_secret = self.root / "access-key-secret"
        self.key_id.write_text("test-access-key-id\n", encoding="utf-8")
        self.key_secret.write_text("test-access-key-secret\n", encoding="utf-8")
        self.config = {
            "domain": "cdn.ichenj.com",
            "dns_zone": "ichenj.com",
            "access_key_id_file": str(self.key_id),
            "access_key_secret_file": str(self.key_secret),
            "propagation_seconds": 0,
        }
        MODULE.validate_config(self.config)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def certbot_env(self, **values):
        environment = {
            "CERTBOT_DOMAIN": "cdn.ichenj.com",
            "CERTBOT_VALIDATION": "validation-token-1234567890",
        }
        environment.update(values)
        return mock.patch.dict(MODULE.os.environ, environment, clear=False)

    def test_hmac_signature_canonicalization(self) -> None:
        class FixedDateTime:
            @classmethod
            def now(cls, tz):
                return cls()

            def strftime(self, _format):
                return "2026-09-06T00:00:00Z"

        nonce = "00000000-0000-0000-0000-000000000000"
        with mock.patch.object(MODULE.uuid, "uuid4", return_value=nonce):
            with mock.patch.object(MODULE.datetime, "datetime", FixedDateTime):
                params = MODULE.signed_params(
                    {
                        "Action": "DescribeDomainRecords",
                        "Version": "2015-01-09",
                        "DomainName": "example.com",
                    },
                    "testid",
                    "testsecret",
                )
        unsigned = {key: value for key, value in params.items() if key != "Signature"}
        self.assertEqual(
            MODULE.quote("a value/with+symbols"),
            "a%20value%2Fwith%2Bsymbols",
        )
        self.assertEqual(
            "&".join(
                MODULE.quote(key) + "=" + MODULE.quote(value)
                for key, value in sorted(unsigned.items())
            ),
            "AccessKeyId=testid&Action=DescribeDomainRecords&DomainName=example.com&Format=JSON&SignatureMethod=HMAC-SHA1&SignatureNonce=00000000-0000-0000-0000-000000000000&SignatureVersion=1.0&Timestamp=2026-09-06T00%3A00%3A00Z&Version=2015-01-09",
        )
        self.assertEqual(params["Signature"], "HKoNh/R13c9QTcZf9T1kfvWI8Iw=")

    def test_auth_adds_exact_txt_and_preserves_existing_txt(self) -> None:
        api = FakeAPI(
            {
                "DescribeDomainRecords": {
                    "TotalCount": 2,
                    "DomainRecords": {
                        "Record": [
                            {
                                "RecordId": "101",
                                "DomainName": "ichenj.com",
                                "RR": "_acme-challenge.cdn",
                                "Type": "TXT",
                                "Value": "existing-token",
                            },
                            {
                                "RecordId": "102",
                                "DomainName": "ichenj.com",
                                "RR": "_acme-challenge.cdn",
                                "Type": "TXT",
                                "Value": "another-token",
                            },
                        ]
                    },
                },
                "AddDomainRecord": {"RecordId": "103"},
            }
        )
        with self.certbot_env():
            output = io.StringIO()
            with redirect_stdout(output):
                MODULE.auth(api, self.config)
        self.assertEqual(output.getvalue(), "103\n")
        self.assertEqual(
            [action for _service, action, _params in api.calls],
            ["DescribeDomainRecords", "AddDomainRecord"],
        )
        add_params = api.calls[-1][2]
        self.assertEqual(
            add_params,
            {
                "DomainName": "ichenj.com",
                "RR": "_acme-challenge.cdn",
                "Type": "TXT",
                "Value": "validation-token-1234567890",
                "TTL": 600,
            },
        )
        self.assertNotIn("DeleteDomainRecord", [action for _service, action, _params in api.calls])

    def test_cleanup_deletes_only_the_auth_record(self) -> None:
        api = FakeAPI(
            {
                "DescribeDomainRecords": {
                    "TotalCount": 2,
                    "DomainRecords": {
                        "Record": [
                            {
                                "RecordId": "201",
                                "DomainName": "ichenj.com",
                                "RR": "_acme-challenge.cdn",
                                "Type": "TXT",
                                "Value": "old-token",
                            },
                            {
                                "RecordId": "202",
                                "DomainName": "ichenj.com",
                                "RR": "_acme-challenge.cdn",
                                "Type": "TXT",
                                "Value": "validation-token-1234567890",
                            },
                        ]
                    },
                },
                "DeleteDomainRecord": {"RequestId": "delete-request"},
            }
        )
        with self.certbot_env(CERTBOT_AUTH_OUTPUT="202\n"):
            MODULE.cleanup(api, self.config)
        self.assertEqual(api.calls[-1], ("alidns", "DeleteDomainRecord", {"RecordId": "202"}))

    def test_cleanup_refuses_domain_rr_type_or_value_mismatch(self) -> None:
        base = {
            "RecordId": "202",
            "DomainName": "ichenj.com",
            "RR": "_acme-challenge.cdn",
            "Type": "TXT",
            "Value": "validation-token-1234567890",
        }
        for field, value in (
            ("DomainName", "other.example"),
            ("RR", "_acme-challenge.other"),
            ("Type", "CNAME"),
            ("Value", "different-token"),
        ):
            with self.subTest(field=field):
                record = dict(base)
                record[field] = value
                api = FakeAPI(
                    {
                        "DescribeDomainRecords": {
                            "TotalCount": 1,
                            "DomainRecords": {"Record": [record]},
                        }
                    }
                )
                with self.certbot_env(CERTBOT_AUTH_OUTPUT="202\n"):
                    try:
                        MODULE.cleanup(api, self.config)
                    except (ValueError, RuntimeError):
                        pass
                self.assertNotIn(
                    "DeleteDomainRecord",
                    [action for _service, action, _params in api.calls],
                )

    def _valid_deploy_inputs(self):
        lineage = self.root / "lineage"
        lineage.mkdir()
        (lineage / "fullchain.pem").write_text("PUBLIC CERTIFICATE\n", encoding="utf-8")
        private_key = "PRIVATE KEY CONTENT MUST NOT BE PRINTED"
        (lineage / "privkey.pem").write_text(private_key, encoding="utf-8")

        def fake_openssl(*args, **_kwargs):
            argv = args[0]
            if "-ext" in argv:
                return mock.Mock(returncode=0, stdout=b"DNS:cdn.ichenj.com\n")
            if "-pubkey" in argv:
                return mock.Mock(returncode=0, stdout=b"PUBLIC-KEY\n")
            if "-pubout" in argv:
                return mock.Mock(returncode=0, stdout=b"PUBLIC-KEY\n")
            if "-issuer" in argv:
                return mock.Mock(returncode=0, stdout=b"issuer=Let's Encrypt\n")
            return mock.Mock(returncode=0, stdout=b"")

        return lineage, private_key, fake_openssl

    def test_deploy_rejects_non_configured_domains(self) -> None:
        lineage, private_key, fake_openssl = self._valid_deploy_inputs()
        environment = {
            "RENEWED_LINEAGE": str(lineage),
            "RENEWED_DOMAINS": "cdn.ichenj.com other.example",
        }
        api = FakeAPI({"SetCdnDomainSSLCertificate": {}})
        with mock.patch.object(MODULE.subprocess, "run", side_effect=fake_openssl):
            with self.assertRaisesRegex(ValueError, "Renewed domains"):
                with mock.patch.dict(MODULE.os.environ, environment, clear=False):
                    MODULE.deploy(api, self.config)
        self.assertEqual(api.calls, [])
        self.assertNotIn(private_key, " ".join(str(call) for call in api.calls))

    def test_deploy_uploads_configured_domain_and_does_not_print_private_key(self) -> None:
        lineage, private_key, fake_openssl = self._valid_deploy_inputs()
        api = FakeAPI({"SetCdnDomainSSLCertificate": {}})
        environment = {
            "RENEWED_LINEAGE": str(lineage),
            "RENEWED_DOMAINS": "cdn.ichenj.com",
        }
        output = io.StringIO()
        with mock.patch.object(MODULE.subprocess, "run", side_effect=fake_openssl):
            with mock.patch.dict(MODULE.os.environ, environment, clear=False):
                with redirect_stdout(output):
                    MODULE.deploy(api, self.config)
        self.assertEqual(api.calls[0][1], "SetCdnDomainSSLCertificate")
        params = api.calls[0][2]
        self.assertEqual(params["DomainName"], "cdn.ichenj.com")
        self.assertEqual(params["CertType"], "upload")
        self.assertEqual(params["SSLProtocol"], "on")
        self.assertEqual(params["SSLPub"], "PUBLIC CERTIFICATE\n")
        self.assertEqual(params["SSLPri"], private_key)
        self.assertNotIn(private_key, output.getvalue())

    def test_deploy_skips_upload_when_current_cdn_certificate_matches(self) -> None:
        lineage, private_key, fake_openssl = self._valid_deploy_inputs()
        api = FakeAPI({"SetCdnDomainSSLCertificate": {}})
        environment = {
            "RENEWED_LINEAGE": str(lineage),
            "RENEWED_DOMAINS": "cdn.ichenj.com",
        }
        output = io.StringIO()
        with mock.patch.object(MODULE.subprocess, "run", side_effect=fake_openssl):
            with mock.patch.object(MODULE, "deployed_matches", return_value=True) as matches:
                with mock.patch.dict(MODULE.os.environ, environment, clear=False):
                    with redirect_stdout(output):
                        MODULE.deploy(api, self.config)
        matches.assert_called_once_with("cdn.ichenj.com", "PUBLIC CERTIFICATE\n")
        self.assertEqual(api.calls, [])
        self.assertIn("already serves", output.getvalue())
        self.assertNotIn(private_key, output.getvalue())

    def test_api_error_output_has_safe_fields_without_credentials(self) -> None:
        class Response:
            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return False

            def read(self):
                return json.dumps(
                    {
                        "Code": "Forbidden",
                        "RequestId": "request-id",
                        "Message": "denied test-access-key-id test-access-key-secret",
                    }
                ).encode()

        api = MODULE.API(self.config)
        with mock.patch.object(MODULE.urllib.request, "urlopen", return_value=Response()):
            with self.assertRaises(RuntimeError) as raised:
                api.call("alidns", "DescribeDomainRecords", DomainName="ichenj.com")
        output = str(raised.exception)
        self.assertIn("Forbidden", output)
        self.assertIn("request-id", output)
        self.assertNotIn("denied", output)
        self.assertNotIn("test-access-key-id", output)
        self.assertNotIn("test-access-key-secret", output)
        self.assertNotIn("https://alidns.aliyuncs.com", output)


if __name__ == "__main__":
    unittest.main()
