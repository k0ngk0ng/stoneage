/* Included once, after battle_event.c's native capture implementation. It
 * reads the ACTUALLY LOADED NeedEnemy table; no RNG, item deletion or battle
 * command is executed by this observer. */
#include <stdio.h>
#include <stdarg.h>

static int StoneAge_CaptureAppend(char **p, size_t *left, const char *format, ...)
{
    int n;
    va_list ap;
    va_start(ap, format);
    n = vsnprintf(*p, *left, format, ap);
    va_end(ap);
    if (n < 0 || (size_t)n >= *left) return 0;
    *p += n;
    *left -= n;
    return 1;
}

char *StoneAge_CaptureObservation(int c, const char *request)
{
    static char buffer[16384];
    char *out = buffer;
    size_t left = sizeof(buffer);
    int b, self, free_slots = 0, slot, target, enemy, ti, n, i, j, id, item;
    int needs[15], used[CHAR_MAXITEMHAVE], eligible, comma;
    if (request && (strlen(request) != 16 || strspn(request, "0123456789abcdef") != 16)) return NULL;
    if (!CHAR_CHECKINDEX(c) || CHAR_getInt(c, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER) return NULL;
    if (!StoneAge_CaptureAppend(&out, &left, "BCAP|v=1")) return NULL;
    if (request && !StoneAge_CaptureAppend(&out, &left, "|request=%s", request)) return NULL;
    b = CHAR_getWorkInt(c, CHAR_WORKBATTLEINDEX);
    if (!BATTLE_CHECKINDEX(b) || !CHAR_getWorkInt(c, CHAR_WORKBATTLEMODE) || BattleArray[b].type != BATTLE_TYPE_P_vs_E) {
        return StoneAge_CaptureAppend(&out, &left, "|active=0") ? buffer : NULL;
    }
    self = BATTLE_Index2No(b, c);
    if (self < 0 || self >= BATTLE_ENTRY_MAX*2) return NULL;
    for (slot = 0; slot < CHAR_MAXPETHAVE; slot++) {
        if (!CHAR_CHECKINDEX(CHAR_getCharPet(c, slot))) free_slots++;
    }
    if (!StoneAge_CaptureAppend(&out, &left, "|active=1|battle=%d|turn=%d|self=%d|free=%d", b, BattleArray[b].turn, self, free_slots)) return NULL;
    for (target = (1-self/BATTLE_ENTRY_MAX)*BATTLE_ENTRY_MAX; target < (2-self/BATTLE_ENTRY_MAX)*BATTLE_ENTRY_MAX; target++) {
        enemy = BATTLE_No2Index(b, target);
        if (!CHAR_CHECKINDEX(enemy) || CHAR_getInt(enemy, CHAR_WHICHTYPE) != CHAR_TYPEENEMY || CHAR_getInt(enemy, CHAR_HP) <= 0) continue;
        n = 0;
        memset(used, 0, sizeof(used));
        ti = IsNeedCaptureItem(enemy);
        if (ti >= 0) {
#ifdef _CAPTURE_FREES
            for (i = 0; i < MAXCAPTRUEFREE && i < 15; i++) {
                id = NeedEnemy[ti].ItemId[i];
                if (id == -1) break;
                for (j = 0; j < n && needs[j] != id; j++) {}
                if (j == n) needs[n++] = id;
            }
#else
            needs[n++] = ti;
#endif
        }
        for (i = 0; i < n; i++) {
#ifdef _CAPTURE_FREES
            j = 0;
#else
            j = CHAR_STARTITEMARRAY;
#endif
            for (; j < CHAR_MAXITEMHAVE; j++) {
                item = CHAR_getItemIndex(c, j);
                if (ITEM_CHECKINDEX(item) && ITEM_getInt(item, ITEM_ID) == needs[i]) used[j] = 1;
            }
        }
        eligible = free_slots > 0 && CHAR_getWorkInt(enemy, CHAR_WORK_PETFLG) != 0 &&
            (CHAR_getWorkInt(c, CHAR_PickAllPet) == TRUE || CHAR_getInt(c, CHAR_LV)+5 >= CHAR_getInt(enemy, CHAR_LV)) &&
            BATTLE_CaptureItemCheck(c, enemy);
        if (!StoneAge_CaptureAppend(&out, &left, "|target=%d,%d,%d,%d,%d,", target,
                CHAR_getInt(enemy, CHAR_PETID), CHAR_getInt(enemy, CHAR_LV),
                CHAR_getInt(enemy, CHAR_BASEIMAGENUMBER), eligible ? 1 : 0)) return NULL;
        for (i = 0; i < n; i++) if (!StoneAge_CaptureAppend(&out, &left, "%s%d", i ? ";" : "", needs[i])) return NULL;
        if (!n && !StoneAge_CaptureAppend(&out, &left, "-")) return NULL;
        if (!StoneAge_CaptureAppend(&out, &left, ",")) return NULL;
        comma = 0;
        for (slot = 0; slot < CHAR_MAXITEMHAVE; slot++) if (used[slot]) {
            item = CHAR_getItemIndex(c, slot);
            if (!StoneAge_CaptureAppend(&out, &left, "%s%d:%d", comma ? ";" : "", slot, ITEM_getInt(item, ITEM_ID))) return NULL;
            comma++;
        }
        if (!comma && !StoneAge_CaptureAppend(&out, &left, "-")) return NULL;
    }
    return buffer;
}
