#!/usr/bin/env python3
"""Replace obsolete StoneAge 2.5 login adverts without transcoding char.c."""

from pathlib import Path
import sys


MARKER = b"STONEAGE_LOCAL_LOGIN_ANNOUNCEMENT"
START = b"#ifdef _VIP_ALL"
END = b"#endif"
OLD_ADVERT = "您现在使用的Server是XFei开发的Windows平台版.".encode("cp936")


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} PATH/TO/char.c", file=sys.stderr)
        return 2

    target = Path(sys.argv[1])
    data = target.read_bytes()
    if MARKER in data:
        return 0
    if data.count(OLD_ADVERT) != 1:
        raise SystemExit(
            f"obsolete login advert: expected one match in {target}, "
            f"found {data.count(OLD_ADVERT)}"
        )

    advert_at = data.index(OLD_ADVERT)
    start = data.rfind(START, 0, advert_at)
    if start < 0:
        raise SystemExit("could not locate _VIP_ALL block start")
    end = data.find(END, advert_at)
    if end < 0:
        raise SystemExit("could not locate _VIP_ALL block end")
    end += len(END)

    replacement = """#ifdef _VIP_ALL
    /* STONEAGE_LOCAL_LOGIN_ANNOUNCEMENT
     * The archived server advertised an obsolete private-server seller and
     * website to every player at login.  Keep a short, project-owned welcome
     * message and preserve the client's traditional-Chinese UI style.
     */
    {
        int i;
        int playernum = CHAR_getPlayerMaxNum();
        char *serverName = getGameserverID();
        char *playerName = CHAR_getChar( charaindex, CHAR_NAME );
        char message[256];
        char clockText[80];
        time_t now = time( 0 );

        strcpy( clockText, ctime( &now ));
        clockText[strlen( clockText ) - 1] = '\\0';

        for( i = 0; i < playernum; i++ ) {
            if( CHAR_getCharUse( i ) == FALSE ) continue;
            snprintf( message, sizeof( message ),
                      "歡迎 %s 登入 %s。", playerName, serverName );
            CHAR_talkToCli( i, -1, message, CHAR_COLORRED );
            snprintf( message, sizeof( message ),
                      "%s 伺服器時間：%s。", serverName, clockText );
            CHAR_talkToCli( i, -1, message, CHAR_COLORRED );
            CHAR_talkToCli( i, -1,
                           "StoneAge Revival 2.5 本機伺服器已連線。",
                           CHAR_COLORYELLOW );
        }
    }
#endif""".encode("cp936")

    updated = data[:start] + replacement + data[end:]
    if OLD_ADVERT in updated or MARKER not in updated:
        raise SystemExit("internal error while replacing login announcement")
    target.write_bytes(updated)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
