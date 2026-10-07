#include <stdint.h>
#include <string.h>
#include <sys/io.h>
#include <unistd.h>

#define DEBUG_EXIT_PORT 0xf4

int main(int argc, char **argv) {
    uint32_t value;
    if (argc == 2 && strcmp(argv[1], "0x10") == 0) {
        value = 0x10;
    } else if (argc == 2 && strcmp(argv[1], "0x11") == 0) {
        value = 0x11;
    } else {
        static const char message[] = "usage: debug-exit 0x10|0x11\n";
        if (write(STDERR_FILENO, message, sizeof(message) - 1) != (ssize_t)(sizeof(message) - 1)) {
            return 3;
        }
        return 2;
    }
    if (ioperm(DEBUG_EXIT_PORT, 4, 1) != 0) {
        static const char message[] = "ioperm debug-exit port failed\n";
        if (write(STDERR_FILENO, message, sizeof(message) - 1) != (ssize_t)(sizeof(message) - 1)) {
            return 3;
        }
        return 1;
    }

    outl(value, DEBUG_EXIT_PORT);
    // QEMU can process the shutdown request asynchronously. Do not return to
    // PID1 and emit a second terminal result while that request is pending.
    for (;;) {
        pause();
    }
}
