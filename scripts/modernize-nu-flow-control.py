#!/usr/bin/env python3
"""Make the legacy NU movement flow-control check configurable.

The archived C sources are mostly GBK/byte-oriented rather than UTF-8. Keep
the rewrite byte-preserving and make it idempotent so repeated server builds
do not apply the same change twice.
"""

from pathlib import Path
import sys


def replace_once(path: Path, old: bytes, new: bytes) -> None:
    data = path.read_bytes()
    if new in data:
        return
    count = data.count(old)
    if count != 1:
        raise SystemExit(
            f"{path}: expected one occurrence of {old!r}, found {count}"
        )
    path.write_bytes(data.replace(old, new, 1))


def ensure_setup_option(path: Path) -> None:
    data = path.read_bytes()
    if b"enable_nu_flow_control=" in data:
        return
    replace_once(
        path,
        b"erruser_down=1\n",
        b"erruser_down=1\n"
        b"# enable_nu_flow_control: 1=enable, 0=disable (default)\n"
        b"enable_nu_flow_control=0\n",
    )


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} PATH/TO/gmsv", file=sys.stderr)
        return 2

    gmsv = Path(sys.argv[1])
    configfile = gmsv / "configfile.c"
    config_header = gmsv / "include" / "configfile.h"
    callfromcli = gmsv / "callfromcli.c"
    init = gmsv / "init.c"
    setup = gmsv / "setup.cf"

    replace_once(
        configfile,
        b"  unsigned int    ErrUserDownFlg;\n  //ttom end",
        b"  unsigned int    ErrUserDownFlg;\n"
        b"  int             enable_nu_flow_control;\n"
        b"  //ttom end",
    )
    replace_once(
        configfile,
        b'    { "erruser_down" ,NULL,0,(void*)&config.ErrUserDownFlg,INT},    \n',
        b'    { "erruser_down" ,NULL,0,(void*)&config.ErrUserDownFlg,INT},    \n'
        b'    { "enable_nu_flow_control" ,NULL,0,\n'
        b'      (void*)&config.enable_nu_flow_control,INT},\n',
    )
    replace_once(
        configfile,
        b"unsigned int getErrUserDownFlg( void )\n"
        b"{\n"
        b"    return config.ErrUserDownFlg;\n"
        b"}\n",
        b"unsigned int getErrUserDownFlg( void )\n"
        b"{\n"
        b"    return config.ErrUserDownFlg;\n"
        b"}\n\n"
        b"int getNuFlowControl( void )\n"
        b"{\n"
        b"    return config.enable_nu_flow_control;\n"
        b"}\n",
    )
    replace_once(
        config_header,
        b"unsigned int getErrUserDownFlg( void );\n",
        b"unsigned int getErrUserDownFlg( void );\n"
        b"int getNuFlowControl( void );\n",
    )
    replace_once(
        callfromcli,
        b"\tif (checkNu(fd)<0) {",
        b"\tif (getNuFlowControl() && checkNu(fd)<0) {",
    )
    replace_once(
        init,
        b"getProtocolreadfrequency());\n",
        b"getProtocolreadfrequency());\n"
        b'        print("NU flow control: %d\\n", getNuFlowControl());\n',
    )
    ensure_setup_option(setup)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
