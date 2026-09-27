/* Test-only translation unit: exercise SQLite commit boundaries without
 * introducing fault injection entry points into the game server. */
#include "../stoneage_ladder_core.c"

static int exit_before_commit(void *unused)
{
    (void)unused;
    _exit(73);
}

static int exit_after_commit(void *unused,sqlite3 *connection,const char *name,int pages)
{
    (void)unused;(void)connection;(void)name;(void)pages;
    _exit(74);
}

void Ladder_TestExitAtCommit(int after)
{
    if(after)sqlite3_wal_hook(db,exit_after_commit,NULL);
    else sqlite3_commit_hook(db,exit_before_commit,NULL);
}
