#ifndef STONEAGE_SAAC_ADMIN_BRIDGE_H
#define STONEAGE_SAAC_ADMIN_BRIDGE_H

/* The queue is on the dedicated player-admin control volume. */
#define STONEAGE_SAAC_ADMIN_BRIDGE_DIR "/run/stoneage/player-admin/saac"

int stoneage_admin_bridge_init(void);
void stoneage_admin_bridge_poll(void);

#endif
