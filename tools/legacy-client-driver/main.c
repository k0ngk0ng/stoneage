#define WIN32_LEAN_AND_MEAN
#include <windows.h>

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    HWND hwnd;
    LONG area;
    int occurrence;
    int seen;
} find_window_context;

/* When two legacy clients are open at once (for example player_demo and
   FriendHero), both surfaces have the same size and title.  The default
   remains the first surface for backwards compatibility, while the
   occurrence override lets the test driver address the second one without
   changing the game client itself. */
static int requested_window_occurrence(void)
{
    const char *value = getenv("STONEAGE_WINDOW_OCCURRENCE");
    char *end = NULL;
    long occurrence;

    if (!value || !*value) return 0;
    occurrence = strtol(value, &end, 10);
    if (!end || *end != '\0' || occurrence < 0 || occurrence > 32) return 0;
    return (int)occurrence;
}

static BOOL CALLBACK find_stoneage_window(HWND hwnd, LPARAM parameter)
{
    find_window_context *context = (find_window_context *)parameter;
    char title[256] = {0};
    RECT client = {0};

    if (!IsWindowVisible(hwnd)) {
        return TRUE;
    }
    /* A client launched in a Wine virtual desktop can briefly expose an empty
       title while cnc-ddraw creates the surface.  The size check below is the
       reliable discriminator, so do not reject that window on title text. */
    GetWindowTextA(hwnd, title, sizeof(title));
    if (!GetClientRect(hwnd, &client)) {
        return TRUE;
    }

    /* The localized title varies between states as well as clients.  In this
       2.5 build it changes from ASCII "StoneAge" to CP936 full-width text
       after WGS login, so title-byte matching cannot be reliable.  The
       dedicated Wine prefix has one visible top-level 4:3 game surface. */
    /* cnc-ddraw can expose a letterboxed 16:9 client surface on modern
       Wine even though the legacy game itself renders a 4:3 viewport.  Do
       not require an exact aspect ratio here; select the largest game-sized
       top-level surface instead. */
    if (client.right >= 640 && client.bottom >= 480) {
        LONG area = client.right * client.bottom;
        if (context->seen++ == context->occurrence) {
            context->hwnd = hwnd;
            context->area = area;
        }
    }
    return TRUE;
}

static HWND wait_for_stoneage_window(DWORD timeout_ms)
{
    DWORD deadline = GetTickCount() + timeout_ms;

    do {
        find_window_context context = {0};
        context.occurrence = requested_window_occurrence();
        EnumWindows(find_stoneage_window, (LPARAM)&context);
        if (context.hwnd) {
            return context.hwnd;
        }
        Sleep(100);
    } while ((LONG)(deadline - GetTickCount()) > 0);

    return NULL;
}

static void send_ascii(HWND hwnd, const char *text)
{
    const unsigned char *character = (const unsigned char *)text;
    while (*character) {
        SendMessageA(hwnd, WM_CHAR, *character++, 1);
        Sleep(35);
    }
}

static void click_game(HWND hwnd, int x, int y)
{
    RECT client = {0};
    int window_x = x;
    int window_y = y;

    /* cnc-ddraw maps input from its resizable output window back into the
       client's virtual 640x480 coordinate system. Feed it output-window
       coordinates so the original WndProc receives the requested game point. */
    if (GetClientRect(hwnd, &client) && client.right > 0 && client.bottom > 0) {
        window_x = MulDiv(x, client.right, 640);
        window_y = MulDiv(y, client.bottom, 480);
    }

    /* The legacy hit-test code reads global mouse state during its render
       tick. Make the selected client foreground before injecting the message
       so a second concurrently running Wine client receives the click. */
    SetForegroundWindow(hwnd);
    SetFocus(hwnd);
    LPARAM point = MAKELPARAM(window_x, window_y);
    SendMessageA(hwnd, WM_MOUSEMOVE, 0, point);
    /* HitDispNo/HitFontNo are produced by the next render tick. Old menu
       controls therefore need one hover frame before the click state arrives. */
    Sleep(250);
    SendMessageA(hwnd, WM_LBUTTONDOWN, MK_LBUTTON, point);
    Sleep(80);
    SendMessageA(hwnd, WM_LBUTTONUP, 0, point);
    /* The click changes focus in the following game tick, not inside the
       window procedure. Wait before sending text to the newly focused field. */
    Sleep(250);
}

static void click_game_button(HWND hwnd, int x, int y, UINT button)
{
    RECT client = {0};
    int window_x = x;
    int window_y = y;
    UINT down_message = button == MK_RBUTTON ? WM_RBUTTONDOWN : WM_LBUTTONDOWN;
    UINT up_message = button == MK_RBUTTON ? WM_RBUTTONUP : WM_LBUTTONUP;

    if (GetClientRect(hwnd, &client) && client.right > 0 && client.bottom > 0) {
        window_x = MulDiv(x, client.right, 640);
        window_y = MulDiv(y, client.bottom, 480);
    }

    SetForegroundWindow(hwnd);
    SetFocus(hwnd);
    LPARAM point = MAKELPARAM(window_x, window_y);
    SendMessageA(hwnd, WM_MOUSEMOVE, 0, point);
    Sleep(250);
    SendMessageA(hwnd, down_message, button, point);
    Sleep(80);
    SendMessageA(hwnd, up_message, 0, point);
    Sleep(250);
}

static void double_click_game(HWND hwnd, int x, int y)
{
    RECT client = {0};
    int window_x = x;
    int window_y = y;
    LPARAM point;

    if (GetClientRect(hwnd, &client) && client.right > 0 && client.bottom > 0) {
        window_x = MulDiv(x, client.right, 640);
        window_y = MulDiv(y, client.bottom, 480);
    }

    point = MAKELPARAM(window_x, window_y);
    SendMessageA(hwnd, WM_MOUSEMOVE, 0, point);
    Sleep(250);

    /* A Win32 double click is down/up, dblclk/up.  Calling click_game twice
       leaves more than the system double-click interval between presses
       because that helper deliberately waits for an old render tick. */
    SendMessageA(hwnd, WM_LBUTTONDOWN, MK_LBUTTON, point);
    Sleep(60);
    SendMessageA(hwnd, WM_LBUTTONUP, 0, point);
    Sleep(100);
    SendMessageA(hwnd, WM_LBUTTONDBLCLK, MK_LBUTTON, point);
    Sleep(60);
    SendMessageA(hwnd, WM_LBUTTONUP, 0, point);
    Sleep(500);
}

static void move_game_pointer(HWND hwnd, int x, int y)
{
    RECT client = {0};
    int window_x = x;
    int window_y = y;

    if (GetClientRect(hwnd, &client) && client.right > 0 && client.bottom > 0) {
        window_x = MulDiv(x, client.right, 640);
        window_y = MulDiv(y, client.bottom, 480);
    }
    SendMessageA(hwnd, WM_MOUSEMOVE, 0, MAKELPARAM(window_x, window_y));
    Sleep(500);
}

static void drag_game_pointer(HWND hwnd, int from_x, int from_y,
                              int to_x, int to_y)
{
    RECT client = {0};
    int window_from_x = from_x;
    int window_from_y = from_y;
    int window_to_x = to_x;
    int window_to_y = to_y;
    int step;

    if (GetClientRect(hwnd, &client) && client.right > 0 && client.bottom > 0) {
        window_from_x = MulDiv(from_x, client.right, 640);
        window_from_y = MulDiv(from_y, client.bottom, 480);
        window_to_x = MulDiv(to_x, client.right, 640);
        window_to_y = MulDiv(to_y, client.bottom, 480);
    }

    SendMessageA(hwnd, WM_MOUSEMOVE, 0,
                 MAKELPARAM(window_from_x, window_from_y));
    Sleep(250);
    SendMessageA(hwnd, WM_LBUTTONDOWN, MK_LBUTTON,
                 MAKELPARAM(window_from_x, window_from_y));
    Sleep(150);
    for (step = 1; step <= 8; ++step) {
        int x = window_from_x + (window_to_x - window_from_x) * step / 8;
        int y = window_from_y + (window_to_y - window_from_y) * step / 8;
        SendMessageA(hwnd, WM_MOUSEMOVE, MK_LBUTTON, MAKELPARAM(x, y));
        Sleep(50);
    }
    Sleep(150);
    SendMessageA(hwnd, WM_LBUTTONUP, 0,
                 MAKELPARAM(window_to_x, window_to_y));
    Sleep(500);
}

static int usage(const char *program)
{
    fprintf(stderr,
            "usage: %s login ACCOUNT PASSWORD\n"
            "       %s fill-login ACCOUNT PASSWORD\n"
            "       %s click X Y\n"
            "       %s right-click X Y\n"
            "       %s double-click X Y\n"
            "       %s move X Y\n"
            "       %s drag FROM_X FROM_Y TO_X TO_Y\n"
            "       %s type TEXT\n"
            "       %s key VIRTUAL_KEY\n"
            "       %s state\n"
            "       %s peek ADDRESS [SIZE]\n",
            program, program, program, program, program, program, program,
            program, program, program, program);
    return 2;
}

static int open_game_process(HWND hwnd, DWORD access, DWORD *process_id,
                             HANDLE *process)
{
    GetWindowThreadProcessId(hwnd, process_id);
    *process = OpenProcess(access, FALSE, *process_id);
    if (!*process) {
        fprintf(stderr, "OpenProcess(%lu) failed: %lu\n",
                (unsigned long)*process_id, (unsigned long)GetLastError());
        return 1;
    }
    return 0;
}

static void print_hex(const char *name, const unsigned char *value, SIZE_T size)
{
    SIZE_T index;

    if (name && *name) {
        printf(" %s=", name);
    }
    for (index = 0; index < size; ++index) {
        printf("%02x", value[index]);
    }
}

static int dump_gateway_state(HWND hwnd)
{
    DWORD process_id = 0;
    HANDLE process;
    DWORD net_initialized = 0;
    DWORD server_chosen = 0;
    DWORD net_write_length = 0;
    DWORD net_read_length = 0;
    DWORD net_write_pointer = 0;
    DWORD net_read_pointer = 0;
    DWORD protocol_dialect = 0;
    DWORD connection_phase = 0;
    DWORD socket_handle = 0;
    DWORD gateway_connected = 0;
    DWORD gateway_server_state = 0;
    DWORD gateway_read_length = 0;
    DWORD gateway_write_length = 0;
    DWORD state = 0;
    DWORD callback_state = 0;
    WORD login_result = 0;
    WORD char_list_result = 0;
    char receive_buffer[64] = {0};
    unsigned char net_write_buffer[128] = {0};
    unsigned char net_read_buffer[128] = {0};
    SIZE_T count = 0;

    if (open_game_process(hwnd, PROCESS_QUERY_INFORMATION | PROCESS_VM_READ,
                          &process_id, &process)) {
        return 1;
    }

#define READ_VALUE(address, value)                                             \
    do {                                                                       \
        if (!ReadProcessMemory(process, (LPCVOID)(address), &(value),           \
                               sizeof(value), &count) ||                        \
            count != sizeof(value)) {                                          \
            fprintf(stderr, "ReadProcessMemory(%p) failed: %lu\n",            \
                    (void *)(address), (unsigned long)GetLastError());          \
            CloseHandle(process);                                              \
            return 1;                                                          \
        }                                                                      \
    } while (0)

    READ_VALUE(0x02B22D78, state);
    READ_VALUE(0x02B22D70, callback_state);
    READ_VALUE(0x02B22D64, login_result);
    READ_VALUE(0x02B22D66, char_list_result);
    READ_VALUE(0x02B228CC, receive_buffer);
    READ_VALUE(0x02B22388, net_initialized);
    READ_VALUE(0x02B2238C, server_chosen);
    READ_VALUE(0x02B22390, net_write_length);
    READ_VALUE(0x02B22398, net_read_length);
    READ_VALUE(0x02B223A4, net_write_pointer);
    READ_VALUE(0x02B223A8, net_read_pointer);
    READ_VALUE(0x02B223C8, protocol_dialect);
    READ_VALUE(0x02F3B588, connection_phase);
    READ_VALUE(0x02B22394, socket_handle);
    READ_VALUE(0x02B2238C, gateway_server_state);
    READ_VALUE(0x02B22398, gateway_read_length);
    READ_VALUE(0x02B22390, gateway_write_length);
    READ_VALUE(0x02B22388, gateway_connected);
#undef READ_VALUE

    if (net_write_pointer != 0 && net_write_length != 0) {
        SIZE_T wanted = net_write_length;
        if (wanted > sizeof(net_write_buffer)) {
            wanted = sizeof(net_write_buffer);
        }
        if (!ReadProcessMemory(process, (LPCVOID)(ULONG_PTR)net_write_pointer,
                               net_write_buffer, wanted, &count)) {
            fprintf(stderr, "ReadProcessMemory(net_write_pointer) failed: %lu\n",
                    (unsigned long)GetLastError());
            CloseHandle(process);
            return 1;
        }
    }
    if (net_read_pointer != 0 && net_read_length != 0) {
        SIZE_T wanted = net_read_length;
        if (wanted > sizeof(net_read_buffer)) {
            wanted = sizeof(net_read_buffer);
        }
        if (!ReadProcessMemory(process, (LPCVOID)(ULONG_PTR)net_read_pointer,
                               net_read_buffer, wanted, &count)) {
            fprintf(stderr, "ReadProcessMemory(net_read_pointer) failed: %lu\n",
                    (unsigned long)GetLastError());
            CloseHandle(process);
            return 1;
        }
    }

    printf("pid=%lu gateway_state=%lu callback_state=%lu login_result=%u "
           "char_list_result=%u net_initialized=%lu server_chosen=%lu "
           "dialect=%lu connection_phase=%lu net_write_length=%lu "
           "net_read_length=%lu socket=%lu gateway_connected=%lu "
           "gateway_server_state=%lu gateway_read_length=%lu "
           "gateway_write_length=%lu net_write_pointer=0x%08lx "
           "net_read_pointer=0x%08lx",
           (unsigned long)process_id, (unsigned long)state,
           (unsigned long)callback_state, (unsigned int)login_result,
           (unsigned int)char_list_result, (unsigned long)net_initialized,
           (unsigned long)server_chosen, (unsigned long)protocol_dialect,
           (unsigned long)connection_phase, (unsigned long)net_write_length,
           (unsigned long)net_read_length, (unsigned long)socket_handle,
           (unsigned long)gateway_connected,
           (unsigned long)gateway_server_state,
           (unsigned long)gateway_read_length,
           (unsigned long)gateway_write_length,
           (unsigned long)net_write_pointer,
           (unsigned long)net_read_pointer);
    print_hex("gateway_receive_hex", (unsigned char *)receive_buffer,
              sizeof(receive_buffer));
    print_hex("net_write_hex", net_write_buffer,
              net_write_length < sizeof(net_write_buffer)
                  ? net_write_length
                  : sizeof(net_write_buffer));
    print_hex("net_read_hex", net_read_buffer,
              net_read_length < sizeof(net_read_buffer)
                  ? net_read_length
                  : sizeof(net_read_buffer));
    printf("\n");
    CloseHandle(process);
    return 0;
}

static int peek_memory(HWND hwnd, const char *address_text,
                       const char *size_text)
{
    DWORD process_id = 0;
    HANDLE process;
    ULONG_PTR address;
    unsigned long requested = 64;
    unsigned char buffer[4096];
    SIZE_T count = 0;
    char *end = NULL;

    address = (ULONG_PTR)strtoul(address_text, &end, 0);
    if (!address || !end || *end != '\0') {
        fprintf(stderr, "invalid address: %s\n", address_text);
        return 2;
    }
    if (size_text) {
        requested = strtoul(size_text, &end, 0);
        if (!end || *end != '\0' || requested == 0 ||
            requested > sizeof(buffer)) {
            fprintf(stderr, "size must be between 1 and %lu\n",
                    (unsigned long)sizeof(buffer));
            return 2;
        }
    }
    if (open_game_process(hwnd, PROCESS_QUERY_INFORMATION | PROCESS_VM_READ,
                          &process_id, &process)) {
        return 1;
    }
    if (!ReadProcessMemory(process, (LPCVOID)address, buffer, requested,
                           &count) || count != requested) {
        fprintf(stderr, "ReadProcessMemory(0x%08lx, %lu) failed: %lu\n",
                (unsigned long)address, requested,
                (unsigned long)GetLastError());
        CloseHandle(process);
        return 1;
    }
    printf("pid=%lu address=0x%08lx size=%lu hex=",
           (unsigned long)process_id, (unsigned long)address, requested);
    for (count = 0; count < requested; ++count) {
        printf("%02x", buffer[count]);
    }
    printf("\n");
    CloseHandle(process);
    return 0;
}

static void fill_login(HWND hwnd, const char *account, const char *password)
{
    click_game(hwnd, 350, 183);
    send_ascii(hwnd, account);

    /* Avoid the old client's fragile Return path. cnc-ddraw translates this
       scaled output-window point back to the virtual 640x480 password box. */
    click_game(hwnd, 350, 214);
    send_ascii(hwnd, password);
}

int main(int argc, char **argv)
{
    HWND hwnd;
    char title[256] = {0};
    RECT client = {0};

    if (argc < 2) {
        return usage(argv[0]);
    }

    hwnd = wait_for_stoneage_window(10000);
    if (!hwnd) {
        fprintf(stderr, "StoneAge window was not found.\n");
        return 1;
    }

    GetWindowTextA(hwnd, title, sizeof(title));
    GetClientRect(hwnd, &client);
    /* stdout is UTF-8 on the macOS host while this title is CP936. Printing
       it as text makes a healthy client look garbled in terminal output. */
    printf("window=0x%p client=%ldx%ld title_cp936_hex=", (void *)hwnd,
           client.right, client.bottom);
    print_hex(NULL, (const unsigned char *)title, strlen(title));
    printf("\n");

    if (strcmp(argv[1], "login") == 0 || strcmp(argv[1], "fill-login") == 0) {
        if (argc != 4) {
            return usage(argv[0]);
        }
        fill_login(hwnd, argv[2], argv[3]);

        if (strcmp(argv[1], "login") == 0) {
            /* Login coordinates are defined by SYSTEM/LOGIN.CPP. */
            click_game(hwnd, 317, 287);
            printf("login submitted\n");
        } else {
            printf("login fields filled without submitting\n");
        }
        return 0;
    }

    if (strcmp(argv[1], "click") == 0) {
        if (argc != 4) {
            return usage(argv[0]);
        }
        click_game(hwnd, atoi(argv[2]), atoi(argv[3]));
        return 0;
    }

    if (strcmp(argv[1], "right-click") == 0) {
        if (argc != 4) {
            return usage(argv[0]);
        }
        click_game_button(hwnd, atoi(argv[2]), atoi(argv[3]), MK_RBUTTON);
        return 0;
    }

    if (strcmp(argv[1], "double-click") == 0) {
        if (argc != 4) {
            return usage(argv[0]);
        }
        double_click_game(hwnd, atoi(argv[2]), atoi(argv[3]));
        return 0;
    }

    if (strcmp(argv[1], "move") == 0) {
        if (argc != 4) {
            return usage(argv[0]);
        }
        move_game_pointer(hwnd, atoi(argv[2]), atoi(argv[3]));
        return 0;
    }

    if (strcmp(argv[1], "drag") == 0) {
        if (argc != 6) {
            return usage(argv[0]);
        }
        drag_game_pointer(hwnd, atoi(argv[2]), atoi(argv[3]),
                          atoi(argv[4]), atoi(argv[5]));
        return 0;
    }

    if (strcmp(argv[1], "type") == 0) {
        if (argc != 3) {
            return usage(argv[0]);
        }
        send_ascii(hwnd, argv[2]);
        return 0;
    }

    if (strcmp(argv[1], "key") == 0) {
        unsigned long key;
        if (argc != 3) {
            return usage(argv[0]);
        }
        key = strtoul(argv[2], NULL, 0);
        /* A real keyboard event is queued on the game window's message
           thread.  SendMessage enters the 2006 WndProc synchronously while
           its render/network loop may be mutating the same globals; the
           Return handler in particular can then crash before transmitting
           chat.  Queue both halves instead, preserving normal Win32 input
           ordering and lParam transition bits. */
        if (!PostMessageA(hwnd, WM_KEYDOWN, key, 1)) {
            fprintf(stderr, "PostMessage(WM_KEYDOWN) failed: %lu\n",
                    (unsigned long)GetLastError());
            return 1;
        }
        Sleep(80);
        if (!PostMessageA(hwnd, WM_KEYUP, key, 0xC0000001)) {
            fprintf(stderr, "PostMessage(WM_KEYUP) failed: %lu\n",
                    (unsigned long)GetLastError());
            return 1;
        }
        Sleep(80);
        return 0;
    }

    if (strcmp(argv[1], "state") == 0) {
        if (argc != 2) {
            return usage(argv[0]);
        }
        return dump_gateway_state(hwnd);
    }

    if (strcmp(argv[1], "peek") == 0) {
        if (argc != 3 && argc != 4) {
            return usage(argv[0]);
        }
        return peek_memory(hwnd, argv[2], argc == 4 ? argv[3] : NULL);
    }

    return usage(argv[0]);
}
