/* Included at the end of char/enemy.c, after the mandatory bounds patch.
 * Reads the tables actually loaded by GMSV, including runtime reloads. Never
 * hashes setup.cf, paths, accounts, or NPC/private character state.
 * FNV-1a is a compatibility checksum, not authentication. */
#ifndef STONEAGE_ENCOUNTER_POLICY_IMPL_H
#define STONEAGE_ENCOUNTER_POLICY_IMPL_H
#include <stdint.h>
#include <stdio.h>

extern int ENCOUNT_encountnum;

static void StoneAge_EncounterPolicyInt(uint64_t *hash, int value)
{
    uint32_t n = (uint32_t)value;
    int i;
    for (i = 0; i < 4; i++) {
        *hash ^= (n >> (i * 8)) & 255;
        *hash *= UINT64_C(1099511628211);
    }
}

const char *StoneAge_EncounterPolicy(void)
{
    static char token[16384];
    uint64_t hash = UINT64_C(14695981039346656037);
    int i, j, group_array, written, found = 0;
    size_t used;
    if (ENCOUNT_encountnum <= 0 || ENCOUNT_table == NULL) return NULL;
    StoneAge_EncounterPolicyInt(&hash, ENCOUNT_encountnum);
    for (i = 0; i < ENCOUNT_encountnum; i++) {
        ENCOUNT_Table *r = &ENCOUNT_table[i];
        StoneAge_EncounterPolicyInt(&hash, r->index);
        StoneAge_EncounterPolicyInt(&hash, r->floor);
        StoneAge_EncounterPolicyInt(&hash, r->rect.x);
        StoneAge_EncounterPolicyInt(&hash, r->rect.y);
        StoneAge_EncounterPolicyInt(&hash, r->rect.width);
        StoneAge_EncounterPolicyInt(&hash, r->rect.height);
        StoneAge_EncounterPolicyInt(&hash, r->encountprob_min);
        StoneAge_EncounterPolicyInt(&hash, r->encountprob_max);
        StoneAge_EncounterPolicyInt(&hash, r->enemymaxnum);
        StoneAge_EncounterPolicyInt(&hash, r->zorder);
        StoneAge_EncounterPolicyInt(&hash, r->event_now);
        StoneAge_EncounterPolicyInt(&hash, r->event_end);
        StoneAge_EncounterPolicyInt(&hash, r->enemy_group);
        for (j = 0; j < ENCOUNT_GROUPMAXNUM; j++) {
            StoneAge_EncounterPolicyInt(&hash, r->groupid[j]);
            StoneAge_EncounterPolicyInt(&hash, r->createprob[j]);
        }
    }
    written = snprintf(token, sizeof(token), "missing-group-abort-v1:%016llx:", (unsigned long long)hash);
    if (written < 0 || (size_t)written >= sizeof(token)) return NULL;
    used = (size_t)written;
    for (i = 0; i < ENCOUNT_encountnum; i++) {
        for (j = 0; j < ENCOUNT_GROUPMAXNUM; j++) {
            int id = ENCOUNT_table[i].groupid[j];
            /* Exact early rejection in ENEMY_getEnemy, even at weight zero
             * and before item/event eligibility. Linking requires 0014. */
            if (id != -1 && !ENEMY_resolveGroupArray(id, &group_array)) {
                /* Table IDs are not unique; use the checksum-bound ordinal. */
                written = snprintf(token + used, sizeof(token) - used, "%s%d", found ? "," : "", i);
                if (written < 0 || (size_t)written >= sizeof(token) - used) return NULL;
                used += (size_t)written;
                found = 1;
                break;
            }
        }
    }
    if (!found) {
        if (used + 1 >= sizeof(token)) return NULL;
        token[used++] = '-';
        token[used] = '\0';
    }
    return token;
}
#endif
