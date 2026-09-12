#!/usr/bin/env python3
"""Exercise exact-peer login admission using the production C implementation."""
from pathlib import Path
import os, subprocess, tempfile
root=Path(__file__).resolve().parents[4]
source=(root/'server/legacy/source/2.5/gmsv/net.c').read_bytes().decode('latin1')
start=source.index('/* STONEAGE_TRUSTED_GATEWAY_SAME_IP')
end=source.index('extern int player_online',start)
code=r'''
#define _DEFAULT_SOURCE
#include <assert.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <netdb.h>
#include <arpa/inet.h>
#define BOOL int
#define TRUE 1
#define FALSE 0
#define NOTLOGIN 0
#define WHILEDOWNLOADCHARLIST 1
#define print(...) ((void)0)
static time_t clock_now=100;
static time_t fixture_time(time_t *out) { if(out)*out=clock_now;return clock_now; }
static int dns_ok=1, dns_calls=0;
static struct in_addr dns_address;
static struct hostent *fixture_gethostbyname(const char *host) {
 static struct hostent entry;
 static char *addresses[2];
 dns_calls++;
 if(!dns_ok || strcmp(host,"gateway")!=0)return NULL;
 addresses[0]=(char *)&dns_address;addresses[1]=NULL;
 memset(&entry,0,sizeof(entry));entry.h_addrtype=AF_INET;
 entry.h_length=sizeof(dns_address);entry.h_addr_list=addresses;return &entry;
}
#define time fixture_time
#define gethostbyname fixture_gethostbyname
static struct { int use,state;struct sockaddr_in sin; } Connect[3];
static const int ConnectLen=3;
''' + source[start:end] + r'''
static unsigned long address(const char *text) { struct in_addr a;assert(inet_aton(text,&a));return a.s_addr; }
static void reset(const char *host) {
 if(host)setenv("STONEAGE_GMSV_TRUSTED_GATEWAY_HOST",host,1);else unsetenv("STONEAGE_GMSV_TRUSTED_GATEWAY_HOST");
 trustedGatewayIPRefreshAt=0;trustedGatewayIP=0;dns_calls=0;dns_ok=1;
 memset(Connect,0,sizeof(Connect));Connect[0].use=1;Connect[0].state=NOTLOGIN;
 Connect[0].sin.sin_addr.s_addr=address("192.0.2.20");dns_address.s_addr=address("192.0.2.20");
}
int main(void) {
 const unsigned long gateway=address("192.0.2.20"),other=address("192.0.2.21");
 reset(NULL);assert(isThereThisIP(gateway)==1);
 Connect[0].state=WHILEDOWNLOADCHARLIST;assert(isThereThisIP(gateway)==1);
 Connect[0].state=2;assert(isThereThisIP(gateway)==0);
 Connect[0].state=NOTLOGIN;Connect[0].use=0;assert(isThereThisIP(gateway)==0);
 reset("gateway");assert(isThereThisIP(gateway)==0);assert(dns_calls==1);
 assert(isThereThisIP(gateway)==0);assert(dns_calls==1);
 Connect[1]=Connect[0];Connect[1].sin.sin_addr.s_addr=other;assert(isThereThisIP(other)==1);
 clock_now+=6;dns_address.s_addr=other;
 assert(isThereThisIP(gateway)==1);assert(isThereThisIP(other)==0);assert(dns_calls==2);
 reset("gateway");dns_ok=0;assert(isThereThisIP(gateway)==1);
 dns_ok=1;assert(isThereThisIP(gateway)==1);assert(dns_calls==1);
 clock_now+=6;assert(isThereThisIP(gateway)==0);assert(dns_calls==2);
 dns_ok=0;clock_now+=6;assert(isThereThisIP(gateway)==1);
 reset("192.0.2.20");assert(isThereThisIP(gateway)==0);assert(dns_calls==0);
 reset("192.0.2.0/16");assert(isThereThisIP(gateway)==1);
 reset("0.0.0.0");assert(isThereThisIP(gateway)==1);
 return 0;
}
'''
tmp=root/'build/gotmp';tmp.mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(dir=tmp,prefix='trusted-gateway-') as directory:
 path=Path(directory);(path/'test.c').write_text(code)
 subprocess.run([os.environ.get('CC','cc'),'-std=c99','-Wall','-Wextra','-Werror',str(path/'test.c'),'-o',str(path/'test')],check=True)
 subprocess.run([str(path/'test')],check=True)
print('GMSV trusted-gateway admission tests passed')
