#ifndef STONEAGE_PLAYER_ADMIN_H
#define STONEAGE_PLAYER_ADMIN_H

/*
 * The web/admin process communicates with GMSV through a private directory
 * mounted into the game container.  This is deliberately a polling API: it
 * runs on the existing GMSV loop and never opens a listening socket.
 */
void STONEAGE_PlayerAdminProcess( void );

#endif
