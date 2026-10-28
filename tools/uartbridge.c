/*
 * uartbridge - relay /dev/ttyS1 <-> a TCP socket, so the protocol logic can run
 * on another machine. NOT read-only: it forwards the client's bytes to the UART
 * unchanged, so the client can send any control or upgrade frame. The bridge adds
 * no authentication, so it listens on 127.0.0.1 by default: reach it through an
 * SSH tunnel (ssh -N -L 9113:127.0.0.1:9113 admin@<miner>). A bind address of
 * 0.0.0.0 exposes the UART, K210 upgrade frames included, to the whole network.
 *
 * btcminer owns /dev/ttyS1, so stop it before running this, and restart it after.
 * The port is opened with an exclusive flock, so a second uartbridge cannot attach
 * to the same UART while one is running. One client at a time; after a client
 * disconnects it waits for the next one.
 *
 * Clean shutdown: a client can make the bridge exit (so the session script can
 * restart btcminer at once, without waiting for its watchdog) by sending the
 * 4-byte control word "BRXT" as its first bytes on a connection. Real UART frames
 * always start with "CN" (0x43 0x4e), so "BRXT" can never be chip traffic and is
 * never forwarded to the UART. A signal (SIGTERM) also stops it.
 *
 * Build (static riscv64): see tools/uartbridge.nix. Run: uartbridge [port] [dev] [bind_addr]
 */
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <termios.h>
#include <unistd.h>

static int open_port(const char *dev) {
    int fd = open(dev, O_RDWR | O_NOCTTY);
    if (fd < 0) return -1;
    if (flock(fd, LOCK_EX | LOCK_NB) != 0) { close(fd); return -1; }
    struct termios t;
    if (tcgetattr(fd, &t) != 0) { close(fd); return -1; }
    t.c_iflag = 0;
    t.c_oflag = 0;
    t.c_lflag = 0;
    t.c_cflag = CS8 | CREAD | CLOCAL;
    cfsetispeed(&t, B115200); /* after c_cflag: speed lives in c_cflag */
    cfsetospeed(&t, B115200);
    t.c_cc[VMIN] = 1;
    t.c_cc[VTIME] = 0;
    tcflush(fd, TCIFLUSH);
    if (tcsetattr(fd, TCSANOW, &t) != 0) { close(fd); return -1; }
    return fd;
}

int main(int argc, char **argv) {
    int port = (argc > 1) ? atoi(argv[1]) : 9113;
    const char *dev = (argc > 2) ? argv[2] : "/dev/ttyS1";
    const char *bind_addr = (argc > 3) ? argv[3] : "127.0.0.1";

    /* A client that disconnects while UART bytes are in flight must not kill the
     * bridge with SIGPIPE; the failed write is handled like a disconnect. */
    signal(SIGPIPE, SIG_IGN);

    int uart = open_port(dev);
    if (uart < 0) { fprintf(stderr, "open %s: %s\n", dev, strerror(errno)); return 1; }

    int srv = socket(AF_INET, SOCK_STREAM, 0);
    int one = 1;
    setsockopt(srv, SOL_SOCKET, SO_REUSEADDR, &one, sizeof(one));
    struct sockaddr_in a = {0};
    a.sin_family = AF_INET;
    if (inet_pton(AF_INET, bind_addr, &a.sin_addr) != 1) { fprintf(stderr, "bad bind address %s\n", bind_addr); return 1; }
    a.sin_port = htons((unsigned short)port);
    if (bind(srv, (struct sockaddr *)&a, sizeof(a)) != 0) { perror("bind"); return 1; }
    if (listen(srv, 1) != 0) { perror("listen"); return 1; }
    fprintf(stderr, "uartbridge: %s <-> tcp %s:%d, waiting\n", dev, bind_addr, port);

    uint8_t buf[4096];
    int stop = 0;                       /* set when a client sends the BRXT control word */
    static const uint8_t QUIT[4] = {'B', 'R', 'X', 'T'};
    for (;;) {
        int cli = accept(srv, NULL, NULL);
        if (cli < 0) { if (errno == EINTR) continue; perror("accept"); break; }
        setsockopt(cli, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));
        tcflush(uart, TCIFLUSH);
        fprintf(stderr, "uartbridge: client connected\n");
        int first = 1;                  /* only the first bytes may be the control word */
        for (;;) {
            fd_set r; FD_ZERO(&r); FD_SET(uart, &r); FD_SET(cli, &r);
            int mx = uart > cli ? uart : cli;
            if (select(mx + 1, &r, NULL, NULL, NULL) < 0) break;
            if (FD_ISSET(cli, &r)) {
                int n = (int)read(cli, buf, sizeof(buf));
                if (n <= 0) break;
                if (first && n >= 4 && memcmp(buf, QUIT, 4) == 0) {
                    fprintf(stderr, "uartbridge: BRXT received, shutting down\n");
                    stop = 1;
                    break;
                }
                first = 0;
                for (int off = 0; off < n;) { int w = (int)write(uart, buf + off, n - off); if (w <= 0) break; off += w; }
            }
            if (FD_ISSET(uart, &r)) {
                int n = (int)read(uart, buf, sizeof(buf));
                /* select said readable, so 0 or an error means the UART is gone: exit
                 * (the session script restarts btcminer) instead of spinning on it. */
                if (n <= 0) { perror("uartbridge: read uart"); stop = 1; break; }
                for (int off = 0; off < n;) { int w = (int)write(cli, buf + off, n - off); if (w <= 0) break; off += w; }
            }
        }
        close(cli);
        if (stop) break;
        fprintf(stderr, "uartbridge: client gone, waiting for next\n");
    }
    close(srv); close(uart);
    return 0;
}
