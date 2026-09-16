/*
 * StoneAge AI funding capability.
 *
 * Policy files are operator-owned, read-only inputs.  A policy is valid only
 * when its account and save slot match the live character.  Every unlimited
 * NPC charge and every finite external transfer is recorded in a separate
 * ledger. Trade pairs publish a replacement containing all prior records and
 * both new records. No legacy character field is widened or repurposed.
 */
#include "version.h"
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

#include "char.h"
#include "char_base.h"
#include "stoneage_ai_funding.h"

#define STONEAGE_AF_POLICY_DIR_ENV "STONEAGE_AI_FUNDING_POLICY_DIR"
#define STONEAGE_AF_LEDGER_ENV "STONEAGE_AI_FUNDING_LEDGER"
#define STONEAGE_AF_DEFAULT_POLICY_DIR "/run/stoneage/ai-funding/policies"
#define STONEAGE_AF_DEFAULT_LEDGER "/run/stoneage/ai-funding/ledger.log"
#define STONEAGE_AF_PATH_MAX 1024
#define STONEAGE_AF_LINE_MAX 768
#define STONEAGE_AF_ACCOUNT_MAX 256
#define STONEAGE_AF_KEY_MAX 64
#define STONEAGE_AF_VALUE_MAX 512
#define STONEAGE_AF_MAX_LIMIT 1000000000L

#define STONEAGE_AF_POLICY_VERSION 1
#define STONEAGE_AF_POLICY_VERSION_BIT 1
#define STONEAGE_AF_ACCOUNT_BIT 2
#define STONEAGE_AF_SLOT_BIT 4
#define STONEAGE_AF_NPC_BIT 8
#define STONEAGE_AF_ENABLED_BIT 16
#define STONEAGE_AF_TRADE_BIT 32
#define STONEAGE_AF_BANK_BIT 64
#define STONEAGE_AF_DROP_BIT 128

typedef struct tagStoneAgeAFPolicy {
    char account[STONEAGE_AF_ACCOUNT_MAX];
    int slot;
    int npc_unlimited;
    int enabled;
    long trade_limit;
    long bank_limit;
    long drop_limit;
} StoneAgeAFPolicy;

/* 1 means a valid active policy, 0 means ordinary-player behavior. */
static int StoneAgeAF_loadPolicy( int charaindex, StoneAgeAFPolicy *policy );

static int StoneAgeAF_isNameChar( int c )
{
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
           (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.';
}

static int StoneAgeAF_copyTrim( char *text )
{
    char *start;
    char *end;
    size_t length;

    if( text == NULL ) return FALSE;
    start = text;
    while( *start == ' ' || *start == '\t' ) start++;
    end = start + strlen( start );
    while( end > start && (end[-1] == ' ' || end[-1] == '\t' ||
                           end[-1] == '\r' || end[-1] == '\n') ) end--;
    length = (size_t)(end - start);
    if( start != text ) memmove( text, start, length );
    text[length] = '\0';
    return TRUE;
}

static int StoneAgeAF_validKey( const char *key )
{
    size_t i;
    if( key == NULL || key[0] == '\0' || strlen(key) >= STONEAGE_AF_KEY_MAX ) {
        return FALSE;
    }
    for( i = 0; key[i] != '\0'; i++ ) {
        if( !StoneAgeAF_isNameChar((unsigned char)key[i]) ) return FALSE;
    }
    return TRUE;
}

static int StoneAgeAF_parseLong( const char *text, long *value )
{
    char *end;
    long parsed;

    if( text == NULL || text[0] == '\0' ) return FALSE;
    errno = 0;
    parsed = strtol( text, &end, 10 );
    if( errno != 0 || end == text || *end != '\0' || parsed < 0 ||
        parsed > STONEAGE_AF_MAX_LIMIT ) return FALSE;
    *value = parsed;
    return TRUE;
}

static int StoneAgeAF_parseSlot( const char *text, int *slot )
{
    long value;
    if( !StoneAgeAF_parseLong(text, &value) || value > 1 ) return FALSE;
    *slot = (int)value;
    return TRUE;
}

static int StoneAgeAF_parsePolicyFile( FILE *fp, StoneAgeAFPolicy *policy )
{
    char line[STONEAGE_AF_LINE_MAX];
    char key[STONEAGE_AF_KEY_MAX];
    char value[STONEAGE_AF_VALUE_MAX];
    char *equals;
    char *line_end;
    int seen = 0;
    int version;
    int parsed_slot;
    int enabled;
    long parsed_limit;

    if( fp == NULL || policy == NULL ) return FALSE;
    memset( policy, 0, sizeof(*policy) );
    policy->enabled = TRUE;

    while( fgets(line, sizeof(line), fp) != NULL ) {
        line_end = strchr( line, '\0' );
        if( line_end == line + sizeof(line) - 1 &&
            line[sizeof(line) - 2] != '\n' && !feof(fp) ) return FALSE;
        StoneAgeAF_copyTrim( line );
        if( line[0] == '\0' || line[0] == '#' ) continue;
        equals = strchr( line, '=' );
        if( equals == NULL ) return FALSE;
        *equals = '\0';
        if( strlen(line) >= sizeof(key) ||
            strlen(equals + 1) >= sizeof(value) ) return FALSE;
        strncpy( key, line, sizeof(key) - 1 );
        key[sizeof(key) - 1] = '\0';
        strncpy( value, equals + 1, sizeof(value) - 1 );
        value[sizeof(value) - 1] = '\0';
        StoneAgeAF_copyTrim( key );
        StoneAgeAF_copyTrim( value );
        if( !StoneAgeAF_validKey(key) ) return FALSE;

        if( strcmp(key, "version") == 0 ) {
            if( (seen & STONEAGE_AF_POLICY_VERSION_BIT) ||
                !StoneAgeAF_parseLong(value, &parsed_limit) ||
                parsed_limit != STONEAGE_AF_POLICY_VERSION ) return FALSE;
            version = (int)parsed_limit;
            (void)version;
            seen |= STONEAGE_AF_POLICY_VERSION_BIT;
        } else if( strcmp(key, "account") == 0 ) {
            if( (seen & STONEAGE_AF_ACCOUNT_BIT) || value[0] == '\0' ||
                strlen(value) >= sizeof(policy->account) ) return FALSE;
            strncpy( policy->account, value, sizeof(policy->account) - 1 );
            policy->account[sizeof(policy->account) - 1] = '\0';
            seen |= STONEAGE_AF_ACCOUNT_BIT;
        } else if( strcmp(key, "slot") == 0 ||
                   strcmp(key, "character_slot") == 0 ) {
            if( seen & STONEAGE_AF_SLOT_BIT ) return FALSE;
            if( !StoneAgeAF_parseSlot(value, &parsed_slot) ) return FALSE;
            policy->slot = parsed_slot;
            seen |= STONEAGE_AF_SLOT_BIT;
        } else if( strcmp(key, "npc_spend") == 0 ) {
            if( (seen & STONEAGE_AF_NPC_BIT) ||
                strcmp(value, "unlimited") != 0 ) return FALSE;
            policy->npc_unlimited = TRUE;
            seen |= STONEAGE_AF_NPC_BIT;
        } else if( strcmp(key, "enabled") == 0 ) {
            if( (seen & STONEAGE_AF_ENABLED_BIT) ||
                !StoneAgeAF_parseLong(value, &parsed_limit) ||
                parsed_limit > 1 ) return FALSE;
            enabled = (int)parsed_limit;
            policy->enabled = enabled != 0;
            seen |= STONEAGE_AF_ENABLED_BIT;
        } else if( strcmp(key, "trade_limit") == 0 ) {
            if( (seen & STONEAGE_AF_TRADE_BIT) ||
                !StoneAgeAF_parseLong(value, &parsed_limit) ) return FALSE;
            policy->trade_limit = parsed_limit;
            seen |= STONEAGE_AF_TRADE_BIT;
        } else if( strcmp(key, "bank_limit") == 0 ) {
            if( (seen & STONEAGE_AF_BANK_BIT) ||
                !StoneAgeAF_parseLong(value, &parsed_limit) ) return FALSE;
            policy->bank_limit = parsed_limit;
            seen |= STONEAGE_AF_BANK_BIT;
        } else if( strcmp(key, "drop_limit") == 0 ) {
            if( (seen & STONEAGE_AF_DROP_BIT) ||
                !StoneAgeAF_parseLong(value, &parsed_limit) ) return FALSE;
            policy->drop_limit = parsed_limit;
            seen |= STONEAGE_AF_DROP_BIT;
        } else {
            /* Forward-compatible metadata is ignored after key validation. */
        }
    }
    if( ferror(fp) ) return FALSE;
    if( (seen & (STONEAGE_AF_POLICY_VERSION_BIT |
                 STONEAGE_AF_ACCOUNT_BIT |
                 STONEAGE_AF_SLOT_BIT |
                 STONEAGE_AF_NPC_BIT)) !=
        (STONEAGE_AF_POLICY_VERSION_BIT |
         STONEAGE_AF_ACCOUNT_BIT |
         STONEAGE_AF_SLOT_BIT |
         STONEAGE_AF_NPC_BIT) ) return FALSE;
    if( !policy->enabled || !policy->npc_unlimited ) return FALSE;
    return TRUE;
}

static const char *StoneAgeAF_policyDir( void )
{
    const char *value = getenv(STONEAGE_AF_POLICY_DIR_ENV);
    if( value == NULL || value[0] == '\0' ) return STONEAGE_AF_DEFAULT_POLICY_DIR;
    return value;
}

static const char *StoneAgeAF_ledgerPath( void )
{
    const char *value = getenv(STONEAGE_AF_LEDGER_ENV);
    if( value == NULL || value[0] == '\0' ) return STONEAGE_AF_DEFAULT_LEDGER;
    return value;
}

static int StoneAgeAF_makePolicyPath( const char *account, int slot,
                                      char *path, size_t path_len )
{
    const char *directory;
    size_t i;
    int written;

    if( account == NULL || path == NULL || slot < 0 || slot > 1 ||
        account[0] == '\0' || strlen(account) >= STONEAGE_AF_ACCOUNT_MAX ) {
        return FALSE;
    }
    for( i = 0; account[i] != '\0'; i++ ) {
        if( !StoneAgeAF_isNameChar((unsigned char)account[i]) ) return FALSE;
    }
    directory = StoneAgeAF_policyDir();
    written = snprintf( path, path_len, "%s/%s.%d.policy", directory,
                        account, slot );
    return written >= 0 && (size_t)written < path_len;
}

static int StoneAgeAF_getCharacterIdentity( int charaindex,
                                            char *account, size_t account_len,
                                            int *slot )
{
    const char *live_account;
    int live_slot;

    if( account == NULL || account_len == 0 || slot == NULL ||
        !CHAR_CHECKINDEX(charaindex) ) return FALSE;
    live_account = CHAR_getChar(charaindex, CHAR_CDKEY);
    live_slot = CHAR_getInt(charaindex, CHAR_SAVEINDEXNUMBER);
    if( live_account == NULL || live_account[0] == '\0' ||
        live_slot < 0 || live_slot > 1 || strlen(live_account) >= account_len ) {
        return FALSE;
    }
    for( ; *live_account != '\0'; live_account++ ) {
        if( !StoneAgeAF_isNameChar((unsigned char)*live_account) ) return FALSE;
    }
    live_account = CHAR_getChar(charaindex, CHAR_CDKEY);
    strcpy( account, live_account );
    *slot = live_slot;
    return TRUE;
}

static int StoneAgeAF_loadPolicy( int charaindex, StoneAgeAFPolicy *policy )
{
	char account[STONEAGE_AF_ACCOUNT_MAX];
	char path[STONEAGE_AF_PATH_MAX];
	FILE *fp;
	struct stat policy_stat;
	int slot;
	int valid;

    if( policy == NULL ||
        !StoneAgeAF_getCharacterIdentity(charaindex, account,
                                          sizeof(account), &slot) ||
        !StoneAgeAF_makePolicyPath(account, slot, path, sizeof(path)) ) {
        return FALSE;
    }
	/* The policy is a capability grant. Reject links and non-private files
	 * before opening them, then inspect the opened descriptor as well so a
	 * replacement race cannot turn an ordinary file into a grant. */
	if( lstat(path, &policy_stat) != 0 || !S_ISREG(policy_stat.st_mode) ||
	    (policy_stat.st_mode & 077) != 0 ) return FALSE;
    fp = fopen(path, "r");
    if( fp == NULL ) return FALSE;
	if( fstat(fileno(fp), &policy_stat) != 0 ||
        !S_ISREG(policy_stat.st_mode) || (policy_stat.st_mode & 077) != 0 ) {
		fclose(fp);
		return FALSE;
	}
    valid = StoneAgeAF_parsePolicyFile(fp, policy);
    fclose(fp);
    if( !valid || strcmp(policy->account, account) != 0 ||
        policy->slot != slot ) return FALSE;
    return TRUE;
}

static int StoneAgeAF_kindLimit( const StoneAgeAFPolicy *policy, int kind,
                                 long *limit )
{
    if( policy == NULL || limit == NULL ) return FALSE;
    if( kind == STONEAGE_AI_EXTERNAL_TRADE ) *limit = policy->trade_limit;
    else if( kind == STONEAGE_AI_EXTERNAL_BANK ) *limit = policy->bank_limit;
    else if( kind == STONEAGE_AI_EXTERNAL_DROP ) *limit = policy->drop_limit;
    else return FALSE;
    return TRUE;
}

static int StoneAgeAF_makeLockPath( char *path, size_t path_len )
{
    int written;
    written = snprintf( path, path_len, "%s.lock", StoneAgeAF_ledgerPath() );
    return written >= 0 && (size_t)written < path_len;
}

static int StoneAgeAF_lockLedger( int exclusive, int *lockfd )
{
    char path[STONEAGE_AF_PATH_MAX];
    int fd;
    if( lockfd == NULL || !StoneAgeAF_makeLockPath(path, sizeof(path)) ) return FALSE;
    fd = open(path, O_RDWR | O_CREAT, 0600);
    if( fd < 0 ) return FALSE;
    if( flock(fd, exclusive ? LOCK_EX : LOCK_SH ) != 0 ) {
        close(fd);
        return FALSE;
    }
    *lockfd = fd;
    return TRUE;
}

static void StoneAgeAF_unlockLedger( int lockfd )
{
    if( lockfd >= 0 ) {
        flock(lockfd, LOCK_UN);
        close(lockfd);
    }
}

static int StoneAgeAF_parseLedgerLong( const char *text, long *amount )
{
    char *end;
    long parsed;
    if( text == NULL || text[0] == '\0' ) return FALSE;
    errno = 0;
    parsed = strtol(text, &end, 10);
    if( errno != 0 || end == text || *end != '\0' || parsed <= 0 ||
        parsed > STONEAGE_AF_MAX_LIMIT ) return FALSE;
    *amount = parsed;
    return TRUE;
}

static int StoneAgeAF_readUsedUnlocked( const StoneAgeAFPolicy *policy,
                                        int kind, long *used )
{
    char line[STONEAGE_AF_LINE_MAX];
    char *fields[6];
    char *cursor;
    char *separator;
    char *end;
    char path[STONEAGE_AF_PATH_MAX];
    FILE *fp;
    long amount;
    long total = 0;
    int field_count;
    int slot;
    int line_kind;

    if( policy == NULL || used == NULL ||
        !StoneAgeAF_kindLimit(policy, kind, used) ) return FALSE;
    *used = 0;
    strncpy(path, StoneAgeAF_ledgerPath(), sizeof(path) - 1);
    path[sizeof(path) - 1] = '\0';
    fp = fopen(path, "r");
    if( fp == NULL ) {
        if( errno == ENOENT ) return TRUE;
        return FALSE;
    }
    while( fgets(line, sizeof(line), fp) != NULL ) {
        if( strchr(line, '\n') == NULL && !feof(fp) ) {
            fclose(fp);
            return FALSE;
        }
        cursor = line;
        field_count = 0;
        while( field_count < 6 ) {
            fields[field_count++] = cursor;
            separator = strchr(cursor, '|');
            if( separator == NULL ) break;
            *separator = '\0';
            cursor = separator + 1;
        }
        if( field_count != 6 || strchr(fields[5], '|') != NULL ) {
            fclose(fp);
            return FALSE;
        }
        end = fields[5] + strlen(fields[5]);
        while( end > fields[5] && (end[-1] == '\r' || end[-1] == '\n') ) end--;
        *end = '\0';
        if( strcmp(fields[0], "1") != 0 ||
            strcmp(fields[1], policy->account) != 0 ||
            !StoneAgeAF_parseSlot(fields[2], &slot) || slot != policy->slot ) {
            continue;
        }
        if( strcmp(fields[3], "npc") == 0 ) line_kind = 0;
        else if( strcmp(fields[3], "trade") == 0 ) line_kind = STONEAGE_AI_EXTERNAL_TRADE;
        else if( strcmp(fields[3], "bank") == 0 ) line_kind = STONEAGE_AI_EXTERNAL_BANK;
        else if( strcmp(fields[3], "drop") == 0 ) line_kind = STONEAGE_AI_EXTERNAL_DROP;
        else {
            fclose(fp);
            return FALSE;
        }
        if( !StoneAgeAF_parseLedgerLong(fields[4], &amount) ) {
            fclose(fp);
            return FALSE;
        }
        if( line_kind == kind ) {
            if( total > STONEAGE_AF_MAX_LIMIT - amount ) {
                fclose(fp);
                return FALSE;
            }
            total += amount;
        }
    }
    if( ferror(fp) ) {
        fclose(fp);
        return FALSE;
    }
    fclose(fp);
    *used = total;
    return TRUE;
}

static int StoneAgeAF_writeAll( int fd, const char *text, size_t length )
{
    ssize_t written;
    size_t offset = 0;
    while( offset < length ) {
        written = write(fd, text + offset, length - offset);
        if( written <= 0 ) return FALSE;
        offset += (size_t)written;
    }
    return TRUE;
}

static int StoneAgeAF_appendUnlocked( const StoneAgeAFPolicy *policy,
                                      const char *kind, int amount )
{
    char line[STONEAGE_AF_LINE_MAX];
    int fd;
    int written;
    time_t now;

    if( policy == NULL || kind == NULL || amount <= 0 ||
        amount > STONEAGE_AF_MAX_LIMIT ) return FALSE;
    fd = open(StoneAgeAF_ledgerPath(), O_WRONLY | O_CREAT | O_APPEND, 0600);
    if( fd < 0 ) return FALSE;
    now = time(NULL);
    written = snprintf(line, sizeof(line), "1|%s|%d|%s|%d|%ld\n",
                       policy->account, policy->slot, kind, amount,
                       (long)now);
    if( written < 0 || (size_t)written >= sizeof(line) ||
        !StoneAgeAF_writeAll(fd, line, (size_t)written) || fsync(fd) != 0 ) {
        close(fd);
        return FALSE;
    }
    close(fd);
    return TRUE;
}

static int StoneAgeAF_ledgerReady( void )
{
    int lockfd;
    int fd;
    if( !StoneAgeAF_lockLedger(TRUE, &lockfd) ) return FALSE;
    fd = open(StoneAgeAF_ledgerPath(), O_WRONLY | O_CREAT | O_APPEND, 0600);
    if( fd < 0 ) {
        StoneAgeAF_unlockLedger(lockfd);
        return FALSE;
    }
    close(fd);
    StoneAgeAF_unlockLedger(lockfd);
    return TRUE;
}

static int StoneAgeAF_externalKindName( int kind, const char **name )
{
    if( name == NULL ) return FALSE;
    if( kind == STONEAGE_AI_EXTERNAL_TRADE ) *name = "trade";
    else if( kind == STONEAGE_AI_EXTERNAL_BANK ) *name = "bank";
    else if( kind == STONEAGE_AI_EXTERNAL_DROP ) *name = "drop";
    else return FALSE;
    return TRUE;
}

static int StoneAge_AIFundingPolicyActive( int charaindex,
                                           StoneAgeAFPolicy *policy )
{
    if( !StoneAgeAF_loadPolicy(charaindex, policy) ) return FALSE;
    return policy->enabled && policy->npc_unlimited;
}

int StoneAge_AIFundingCanCharge( int charaindex, int amount )
{
    StoneAgeAFPolicy policy;
    if( amount < 0 ) return FALSE;
    if( !StoneAge_AIFundingPolicyActive(charaindex, &policy) ) {
        return CHAR_CHECKINDEX(charaindex) &&
               CHAR_getInt(charaindex, CHAR_GOLD) >= amount;
    }
    if( amount == 0 ) return TRUE;
    return StoneAgeAF_ledgerReady();
}

int StoneAge_AIFundingCharge( int charaindex, int amount )
{
    StoneAgeAFPolicy policy;
    if( !CHAR_CHECKINDEX(charaindex) ) return FALSE;
    if( amount <= 0 ) return amount == 0;
    if( !StoneAge_AIFundingPolicyActive(charaindex, &policy) ) {
        /* Legacy CHAR_DelGold clamps its requested amount to the carry
         * limit. Reject an unaffordable full quote before calling it. */
        if( CHAR_getInt(charaindex, CHAR_GOLD) < amount ) return FALSE;
        return CHAR_DelGold(charaindex, amount) != 0;
    }
    {
        int lockfd;
        if( !StoneAgeAF_lockLedger(TRUE, &lockfd) ) return FALSE;
        if( !StoneAgeAF_appendUnlocked(&policy, "npc", amount) ) {
            StoneAgeAF_unlockLedger(lockfd);
            return FALSE;
        }
        StoneAgeAF_unlockLedger(lockfd);
    }
    return TRUE;
}

int StoneAge_AIFundingCanExternal( int charaindex, int kind, int amount )
{
    StoneAgeAFPolicy policy;
    const char *name;
    long used;
    long limit;
    int lockfd;

    if( amount < 0 || !CHAR_CHECKINDEX(charaindex) ) return FALSE;
    if( !StoneAgeAF_externalKindName(kind, &name) ) return FALSE;
    if( !StoneAge_AIFundingPolicyActive(charaindex, &policy) ) {
        return CHAR_getInt(charaindex, CHAR_GOLD) >= amount;
    }
    if( CHAR_getInt(charaindex, CHAR_GOLD) < amount ||
        !StoneAgeAF_kindLimit(&policy, kind, &limit) ) return FALSE;
    if( amount == 0 ) return TRUE;
    if( !StoneAgeAF_lockLedger(FALSE, &lockfd) ) return FALSE;
    if( !StoneAgeAF_readUsedUnlocked(&policy, kind, &used) ) {
        StoneAgeAF_unlockLedger(lockfd);
        return FALSE;
    }
    StoneAgeAF_unlockLedger(lockfd);
    if( used > limit || (long)amount > limit - used ) return FALSE;
    return StoneAgeAF_ledgerReady();
}

int StoneAge_AIFundingRecordExternal( int charaindex, int kind, int amount )
{
    StoneAgeAFPolicy policy;
    const char *name;
    long used;
    long limit;
    int lockfd;

    if( amount <= 0 ) return amount == 0;
    if( !StoneAgeAF_externalKindName(kind, &name) ) return FALSE;
    if( !StoneAge_AIFundingPolicyActive(charaindex, &policy) ) return TRUE;
    if( !StoneAgeAF_kindLimit(&policy, kind, &limit) ||
        !StoneAgeAF_lockLedger(TRUE, &lockfd) ) return FALSE;
    if( !StoneAgeAF_readUsedUnlocked(&policy, kind, &used) ||
        used > limit || (long)amount > limit - used ||
        !StoneAgeAF_appendUnlocked(&policy, name, amount) ) {
        StoneAgeAF_unlockLedger(lockfd);
        return FALSE;
    }
    StoneAgeAF_unlockLedger(lockfd);
    return TRUE;
}

/* Caller holds the common ledger lock across validation and publication.
 * Preserve the six-field format for existing readers. Never publish just
 * the first party's debit if preparation of the second debit fails. */
static int StoneAgeAF_publishTradePairUnlocked( const char *records, size_t length )
{
    char temporary[STONEAGE_AF_PATH_MAX];
    char directory[STONEAGE_AF_PATH_MAX];
    char buffer[8192];
    char *slash;
    const char *path = StoneAgeAF_ledgerPath();
    int source = -1, target = -1, parent = -1, ok = FALSE;
    int count, created = FALSE, last = '\n';
    ssize_t n;

    count = snprintf(temporary, sizeof(temporary), "%s.pair.XXXXXX", path);
    if( count < 0 || (size_t)count >= sizeof(temporary) ||
        strlen(path) >= sizeof(directory) ) return FALSE;
    strcpy(directory, path);
    slash = strrchr(directory, '/');
    if( slash == NULL ) strcpy(directory, ".");
    else if( slash == directory ) slash[1] = '\0';
    else *slash = '\0';
    parent = open(directory, O_RDONLY);
    if( parent < 0 ) return FALSE;
    /* Discover unsupported directory syncing before publishing anything. */
    if( fsync(parent) != 0 ) goto done;
    target = mkstemp(temporary);
    if( target < 0 ) goto done;
    created = TRUE;
    source = open(path, O_RDONLY);
    if( source < 0 && errno != ENOENT ) goto done;
    if( source >= 0 ) {
        while( (n = read(source, buffer, sizeof(buffer))) > 0 ) {
            if( !StoneAgeAF_writeAll(target, buffer, (size_t)n) ) goto done;
            last = (unsigned char)buffer[n - 1];
        }
        if( n < 0 || last != '\n' ) goto done;
        if( close(source) != 0 ) { source = -1; goto done; }
        source = -1;
    }
    if( !StoneAgeAF_writeAll(target, records, length) || fsync(target) != 0 ) goto done;
    if( close(target) != 0 ) { target = -1; goto done; }
    target = -1;
    if( rename(temporary, path) != 0 ) goto done;
    temporary[0] = '\0';
    /* A failure here is uncertain durability, not a license to replay. Both
     * records are visible together, but character saves are still separate. */
    ok = fsync(parent) == 0;
done:
    if( source >= 0 ) close(source);
    if( target >= 0 ) close(target);
    if( created && temporary[0] != '\0' ) unlink(temporary);
    if( parent >= 0 ) close(parent);
    return ok;
}

int StoneAge_AIFundingRecordTradePair( int first, int first_amount,
                                      int second, int second_amount )
{
    StoneAgeAFPolicy policies[2];
    int indices[2], amounts[2], active[2];
    int i, lockfd, written, ok = FALSE;
    long used, limit;
    size_t length = 0;
    char records[STONEAGE_AF_LINE_MAX * 2];
    time_t now = time(NULL);

    if( first == second || !CHAR_CHECKINDEX(first) || !CHAR_CHECKINDEX(second) ||
        first_amount < 0 || second_amount < 0 ) return FALSE;
    indices[0] = first; indices[1] = second;
    amounts[0] = first_amount; amounts[1] = second_amount;
    for( i = 0; i < 2; i++ ) {
        if( CHAR_getInt(indices[i], CHAR_GOLD) < amounts[i] ) return FALSE;
        active[i] = StoneAge_AIFundingPolicyActive(indices[i], &policies[i]);
    }
    if( active[0] && active[1] && policies[0].slot == policies[1].slot &&
        strcmp(policies[0].account, policies[1].account) == 0 ) return FALSE;
    if( (!active[0] || amounts[0] == 0) && (!active[1] || amounts[1] == 0) ) return TRUE;
    if( !StoneAgeAF_lockLedger(TRUE, &lockfd) ) return FALSE;
    /* A competing writer may have held the lock while policy or balance
     * changed. Commit from current inputs, not the preflight snapshot. */
    for( i = 0; i < 2; i++ ) {
        if( !CHAR_CHECKINDEX(indices[i]) || CHAR_getInt(indices[i], CHAR_GOLD) < amounts[i] ) goto done;
        active[i] = StoneAge_AIFundingPolicyActive(indices[i], &policies[i]);
    }
    if( active[0] && active[1] && policies[0].slot == policies[1].slot &&
        strcmp(policies[0].account, policies[1].account) == 0 ) goto done;
    for( i = 0; i < 2; i++ ) {
        if( !active[i] || amounts[i] == 0 ) continue;
        if( !StoneAgeAF_kindLimit(&policies[i], STONEAGE_AI_EXTERNAL_TRADE, &limit) ||
            !StoneAgeAF_readUsedUnlocked(&policies[i], STONEAGE_AI_EXTERNAL_TRADE, &used) ||
            used > limit || (long)amounts[i] > limit - used ) goto done;
        written = snprintf(records + length, sizeof(records) - length,
                           "1|%s|%d|trade|%d|%ld\n", policies[i].account,
                           policies[i].slot, amounts[i], (long)now);
        if( written < 0 || (size_t)written >= sizeof(records) - length ) goto done;
        length += (size_t)written;
    }
    ok = length == 0 || StoneAgeAF_publishTradePairUnlocked(records, length);
done:
    StoneAgeAF_unlockLedger(lockfd);
    return ok;
}

void StoneAge_AIFundingRefresh( void )
{
    /* Policies are intentionally uncached so deleting one revokes it now. */
}
