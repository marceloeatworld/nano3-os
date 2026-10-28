/*
 * nano3ctl - minimal, read-only driver for the Avalon Nano 3 K210 "MM" link.
 * Speaks the reverse-engineered K230 <-> K210 protocol on /dev/ttyS1 (115200 8N1).
 *
 * Version 1 is query-only: it sends DETECT (0x10) and POLL (0x33) frames and
 * decodes the replies. It sends NO configuration command (no frequency,
 * voltage, work level, finish or upgrade), so it cannot change the miner.
 *
 * btcminer owns /dev/ttyS1, so stop it before running this and restart it after.
 *
 * Build (static riscv64): see tools/nano3ctl.nix.
 */
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/select.h>
#include <termios.h>
#include <time.h>
#include <unistd.h>

#define MAGIC0 0x43 /* 'C' */
#define MAGIC1 0x4e /* 'N' */
#define HDR 12
#define MAXPL 0x80

static uint16_t crc16_xmodem(const uint8_t *p, int n) {
    uint16_t crc = 0;
    for (int i = 0; i < n; i++) {
        crc ^= (uint16_t)p[i] << 8;
        for (int b = 0; b < 8; b++)
            crc = (crc & 0x8000) ? (uint16_t)((crc << 1) ^ 0x1021) : (uint16_t)(crc << 1);
    }
    return crc;
}

static int open_port(const char *dev) {
    int fd = open(dev, O_RDWR | O_NOCTTY);
    if (fd < 0) return -1;
    /* same exclusive lock as uartbridge, so the two never share the UART
     * (btcminer takes no lock: it must still be stopped by hand) */
    if (flock(fd, LOCK_EX | LOCK_NB) != 0) { close(fd); return -1; }
    struct termios t;
    if (tcgetattr(fd, &t) != 0) { close(fd); return -1; }
    t.c_iflag = 0;
    t.c_oflag = 0;
    t.c_lflag = 0;
    t.c_cflag = CS8 | CREAD | CLOCAL; /* matches btcminer uart_init */
    /* set speed AFTER c_cflag: cfsetspeed writes the baud bits into c_cflag */
    cfsetispeed(&t, B115200);
    cfsetospeed(&t, B115200);
    t.c_cc[VMIN] = 1;
    t.c_cc[VTIME] = 0;
    tcflush(fd, TCIFLUSH);
    if (tcsetattr(fd, TCSANOW, &t) != 0) { close(fd); return -1; }
    return fd;
}

/* Build a frame into out (must hold HDR+len). Returns total length. */
static int build(uint8_t *out, uint8_t type, uint16_t idx, uint16_t cnt,
                 const uint8_t *pl, uint16_t len) {
    out[0] = MAGIC0;
    out[1] = MAGIC1;
    out[4] = type;
    out[5] = 0;
    out[6] = idx & 0xff; out[7] = idx >> 8;
    out[8] = cnt & 0xff; out[9] = cnt >> 8;
    out[10] = len & 0xff; out[11] = len >> 8;
    if (len) memcpy(out + HDR, pl, len);
    uint16_t crc = crc16_xmodem(out + 4, 8 + len);
    out[2] = crc & 0xff; out[3] = crc >> 8;
    return HDR + len;
}

static int g_verbose; /* -v: print raw bytes of every read */

/* Read with a deadline (ms). Returns bytes read into buf (<=cap), 0 on timeout. */
static int read_to(int fd, uint8_t *buf, int cap, int ms) {
    fd_set r; FD_ZERO(&r); FD_SET(fd, &r);
    struct timeval tv = { ms / 1000, (ms % 1000) * 1000 };
    int s = select(fd + 1, &r, NULL, NULL, &tv);
    if (s <= 0) return 0;
    int n = (int)read(fd, buf, cap);
    if (n < 0) return 0;
    if (g_verbose && n > 0) {
        fprintf(stderr, "    raw[%d]: ", n);
        for (int i = 0; i < n; i++) fprintf(stderr, "%02x", buf[i]);
        fprintf(stderr, "\n");
    }
    return n;
}

static uint8_t rxbuf[4096];
static int rxlen;

/* Pull one valid CN frame into frame[], up to deadline. Returns payload len, or -1. */
static int recv_frame(int fd, uint8_t *frame, int deadline_ms) {
    struct timespec t0; clock_gettime(CLOCK_MONOTONIC, &t0);
    for (;;) {
        /* try to cut a frame from what we have */
        int i = 0;
        while (i + 1 < rxlen && !(rxbuf[i] == MAGIC0 && rxbuf[i + 1] == MAGIC1)) i++;
        if (i) { memmove(rxbuf, rxbuf + i, rxlen - i); rxlen -= i; }
        if (rxlen >= HDR) {
            uint16_t len = rxbuf[10] | (rxbuf[11] << 8);
            if (len <= MAXPL && rxlen >= HDR + len) {
                memcpy(frame, rxbuf, HDR + len);
                memmove(rxbuf, rxbuf + HDR + len, rxlen - (HDR + len));
                rxlen -= HDR + len;
                return len;
            }
            if (len > MAXPL) { memmove(rxbuf, rxbuf + 2, rxlen - 2); rxlen -= 2; continue; }
        }
        struct timespec now; clock_gettime(CLOCK_MONOTONIC, &now);
        int el = (int)((now.tv_sec - t0.tv_sec) * 1000 + (now.tv_nsec - t0.tv_nsec) / 1000000);
        if (el >= deadline_ms) return -1;
        int n = read_to(fd, rxbuf + rxlen, (int)sizeof(rxbuf) - rxlen, deadline_ms - el);
        if (n > 0) rxlen += n;
    }
}

static void hexdump(const uint8_t *p, int n) {
    for (int i = 0; i < n; i++) printf("%02x", p[i]);
}

static const char *type_name(uint8_t t) {
    switch (t) {
        case 0x11: return "DETECT_ACK"; case 0x13: return "SYNC_ACK"; case 0x15: return "INFO_RES";
        case 0x50: return "NONCE"; case 0x51: return "STATUS"; case 0x52: return "ASIC_COUNTERS";
        case 0x53: return "FREQ_REPORT"; case 0x54: return "ASIC_PVT"; case 0x55: return "POWER";
        case 0x60: return "GET_STATUS"; case 0x71: return "LEVELPARAM";
        default: return "?";
    }
}

static uint32_t le32(const uint8_t *p) { return p[0] | p[1] << 8 | p[2] << 16 | (uint32_t)p[3] << 24; }

static void decode(const uint8_t *f, int len, int crc_ok) {
    uint8_t t = f[4];
    const uint8_t *p = f + HDR;
    printf("RX %02x %-13s idx=%d cnt=%d len=%d crc=%s  ",
           t, type_name(t), f[6] | f[7] << 8, f[8] | f[9] << 8, len, crc_ok ? "ok" : "BAD");
    if (t == 0x11 && len >= 0x46) {
        char hdna[17] = {0}, hver[33] = {0};
        memcpy(hdna, p + 4, 16);
        memcpy(hver, p + 20, 32);
        /* Walk the strings in a zero-padded copy: a reply without NUL terminators
         * then stops at the padding instead of running past the frame. */
        uint8_t q[MAXPL + 3] = {0};
        memcpy(q, p, len);
        const char *hw = (const char *)q + 0x45;
        int o = 0x45 + (int)strlen(hw) + 1;
        const char *sw = (const char *)q + o;
        int ml = q[o + (int)strlen(sw) + 1];
        printf("HDNA=%s HVERSION=%s HWTYPE=%s SWTYPE=%s max_level=%d", hdna, hver, hw, sw, ml);
    } else if (t == 0x51 && len >= 0x34) {
        char core[8] = {0}; memcpy(core, p + 0x25, 7);
        printf("HashStatus=%d LW=%u GHSmm=%.2f TA=%d Core=%s TMax=%d worklevel=%d TarT=%d",
               p[0], le32(p + 5), le32(p + 9) / 1000.0, (int)le32(p + 0x21), core,
               (int32_t)le32(p + 0x2c), p[0x30], p[0x31]);
    } else if (t == 0x54 && len >= 6) {
        printf("asic=%d temp=%.1f volt=%.1f", f[6] | f[7] << 8,
               (p[0] | p[1] << 8) / 10.0, (p[2] | p[3] << 8) / 10.0);
    } else {
        hexdump(p, len);
    }
    printf("\n");
}

int main(int argc, char **argv) {
    const char *dev = "/dev/ttyS1";
    int polls = 10, do_detect = 1;
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "-v")) g_verbose = 1;
        else if (!strcmp(argv[i], "--poll-only")) do_detect = 0;
        else if (argv[i][0] == '/') dev = argv[i];
        else polls = atoi(argv[i]);
    }
    int fd = open_port(dev);
    if (fd < 0) { fprintf(stderr, "open %s: %s\n", dev, strerror(errno)); return 1; }

    uint8_t frame[HDR + MAXPL], tx[HDR + MAXPL], pl[MAXPL];
    int n;

    /* 1. DETECT (query only) */
    if (do_detect) {
        memset(pl, 0, 4);
        n = build(tx, 0x10, 0, 1, pl, 4);
        printf("TX 10 DETECT\n");
        write(fd, tx, n);
        for (int tries = 0; tries < 5; tries++) {
            int len = recv_frame(fd, frame, 300);
            if (len < 0) { printf("  (no reply, retry)\n"); write(fd, tx, n); continue; }
            int ok = (uint16_t)(frame[2] | frame[3] << 8) == crc16_xmodem(frame + 4, 8 + len);
            decode(frame, len, ok);
            if (ok && frame[4] == 0x11) break;
            write(fd, tx, n); /* stale or corrupt reply: ask again, like a timeout */
        }
    }

    /* 2. POLL loop (query only) */
    uint8_t poll_pl[4] = {0, 0, 0, 0};
    for (int i = 0; i < polls; i++) {
        n = build(tx, 0x33, 0, 1, poll_pl, 4);
        write(fd, tx, n);
        int len = recv_frame(fd, frame, 300);
        if (len < 0) { printf("poll %d: timeout\n", i); continue; }
        int ok = (uint16_t)(frame[2] | frame[3] << 8) == crc16_xmodem(frame + 4, 8 + len);
        decode(frame, len, ok);
        usleep(100000);
    }
    close(fd);
    return 0;
}
