package keyboard

import (
    "syscall"
    "unsafe"
)

const (
    vk_backspace   = 0x8
    vk_tab         = 0x9
    vk_enter       = 0xd
    vk_esc         = 0x1b
    vk_space       = 0x20
    vk_pgup        = 0x21
    vk_pgdn        = 0x22
    vk_end         = 0x23
    vk_home        = 0x24
    vk_arrow_left  = 0x25
    vk_arrow_up    = 0x26
    vk_arrow_right = 0x27
    vk_arrow_down  = 0x28
    vk_insert      = 0x2d
    vk_delete      = 0x2e

    vk_f1          = 0x70
    vk_f2          = 0x71
    vk_f3          = 0x72
    vk_f4          = 0x73
    vk_f5          = 0x74
    vk_f6          = 0x75
    vk_f7          = 0x76
    vk_f8          = 0x77
    vk_f9          = 0x78
    vk_f10         = 0x79
    vk_f11         = 0x7a
    vk_f12         = 0x7b

    right_alt_pressed  = 0x1
    left_alt_pressed   = 0x2
    right_ctrl_pressed = 0x4
    left_ctrl_pressed  = 0x8
    shift_pressed      = 0x10
)


type (
    wchar     uint16
    dword     uint32
    word      uint16

    k32_input struct {
        event_type word
        _          [2]byte
        event      [16]byte
    }

    k32_event struct {
        key_down          int32
        repeat_count      word
        virtual_key_code  word
        virtual_scan_code word
        unicode_char      wchar
        control_key_state dword
    }
)

var (
    kernel32 = syscall.NewLazyDLL("kernel32.dll")

    k32_CreateEventW           = kernel32.NewProc("CreateEventW")
    k32_WaitForMultipleObjects = kernel32.NewProc("WaitForMultipleObjects")
    k32_ReadConsoleInputW      = kernel32.NewProc("ReadConsoleInputW")
    k32_SetEvent               = kernel32.NewProc("SetEvent")

    hConsoleIn syscall.Handle
    hInterrupt syscall.Handle
    eventHandles []syscall.Handle

    input_comm       = make(chan keyEvent)
    cancel_comm      = make(chan bool, 1)
    cancel_done_comm = make(chan bool)
    interrupt_comm   = make(chan struct{})

    // This is just to prevent heap allocs at all costs
    tmpArg dword
)

func getError(errno syscall.Errno) error {
    if errno != 0 {
        return error(errno)
    } else {
        return syscall.EINVAL
    }
}

func getKeyEvent(r *k32_event) (keyEvent, bool) {
    e := keyEvent{}

    if r.key_down == 0 {
        return e, false
    }

    ctrlPressed := r.control_key_state&(left_ctrl_pressed|right_ctrl_pressed) != 0

    if r.virtual_key_code >= vk_f1 && r.virtual_key_code <= vk_f12 {
        switch r.virtual_key_code {
        case vk_f1:
            e.key = KeyF1
        case vk_f2:
            e.key = KeyF2
        case vk_f3:
            e.key = KeyF3
        case vk_f4:
            e.key = KeyF4
        case vk_f5:
            e.key = KeyF5
        case vk_f6:
            e.key = KeyF6
        case vk_f7:
            e.key = KeyF7
        case vk_f8:
            e.key = KeyF8
        case vk_f9:
            e.key = KeyF9
        case vk_f10:
            e.key = KeyF10
        case vk_f11:
            e.key = KeyF11
        case vk_f12:
            e.key = KeyF12
        default:
            panic("unreachable")
        }

        return e, true
    }

    if r.virtual_key_code <= vk_delete {
        switch r.virtual_key_code {
        case vk_insert:
            e.key = KeyInsert
        case vk_delete:
            e.key = KeyDelete
        case vk_home:
            e.key = KeyHome
        case vk_end:
            e.key = KeyEnd
        case vk_pgup:
            e.key = KeyPgup
        case vk_pgdn:
            e.key = KeyPgdn
        case vk_arrow_up:
            e.key = KeyArrowUp
        case vk_arrow_down:
            e.key = KeyArrowDown
        case vk_arrow_left:
            e.key = KeyArrowLeft
        case vk_arrow_right:
            e.key = KeyArrowRight
        case vk_backspace:
            if ctrlPressed {
                e.key = KeyBackspace2
            } else {
                e.key = KeyBackspace
            }
        case vk_tab:
            e.key = KeyTab
        case vk_enter:
            e.key = KeyEnter
        case vk_esc:
            e.key = KeyEsc
        case vk_space:
            if ctrlPressed {
                // manual return here, because KeyCtrlSpace is zero
                e.key = KeyCtrlSpace
                return e, true
            } else {
                e.key = KeySpace
            }
        }

        if e.key != 0 {
            return e, true
        }
    }

    if ctrlPressed {
        if Key(r.unicode_char) >= KeyCtrlA && Key(r.unicode_char) <= KeyCtrlRsqBracket {
            e.key = Key(r.unicode_char)
            return e, true
        }
        switch r.virtual_key_code {
        case 192, 50:
            // manual return here, because KeyCtrl2 is zero
            e.key = KeyCtrl2
            return e, true
        case 51:
            e.key = KeyCtrl3
        case 52:
            e.key = KeyCtrl4
        case 53:
            e.key = KeyCtrl5
        case 54:
            e.key = KeyCtrl6
        case 189, 191, 55:
            e.key = KeyCtrl7
        case 8, 56:
            e.key = KeyCtrl8
        }

        if e.key != 0 {
            return e, true
        }
    }

    if r.unicode_char != 0 {
        e.rune = rune(r.unicode_char)
        return e, true
    }

    return e, false
}

func inputEventsProducer() {
    var input k32_input
    for {
        // Wait for events
        r0, _, e1 := syscall.Syscall6(k32_WaitForMultipleObjects.Addr(), 4,
            uintptr(len(eventHandles)), uintptr(unsafe.Pointer(&eventHandles[0])), 0, 0xFFFFFFFF, 0, 0)
        if uint32(r0) == 0xFFFFFFFF {
            input_comm <- keyEvent{err: getError(e1)}
        }

        select {
        case <-cancel_comm:
            cancel_done_comm <- true
            return
        default:
        }

        // Get console input
        r0, _, e1 = syscall.Syscall6(k32_ReadConsoleInputW.Addr(), 4,
            uintptr(hConsoleIn), uintptr(unsafe.Pointer(&input)), 1, uintptr(unsafe.Pointer(&tmpArg)), 0, 0)
        if int(r0) == 0 {
            input_comm <- keyEvent{err: getError(e1)}
        }

        if input.event_type == 0x1 { // key_event
            kEvent := (*k32_event)(unsafe.Pointer(&input.event))
            ev, ok := getKeyEvent(kEvent)
            if ok {
                for i := 0; i < int(kEvent.repeat_count); i++ {
                    input_comm <- ev
                }
            }
        }
    }
}

func Open() (err error) {
    if (isOpen) {
        return
    }

    // Create an interrupt event
    r0, _, e1 := syscall.Syscall6(k32_CreateEventW.Addr(), 4, 0, 0, 0, 0, 0, 0)
    if int(r0) == 0 {
        return getError(e1)
    }
    hInterrupt = syscall.Handle(r0)

    hConsoleIn, err = syscall.Open("CONIN$", syscall.O_RDWR, 0)
    if err != nil {
        syscall.Close(hInterrupt)
        return
    }

    eventHandles = []syscall.Handle{hConsoleIn, hInterrupt}
    go inputEventsProducer()
    isOpen = true
    return
}

// Should be called after successful initialization when functionality isn't required anymore.
func Close() {
    if (!isOpen) {
        return
    }

    // Stop events producer
    cancel_comm <- true
    syscall.Syscall(k32_SetEvent.Addr(), 1, uintptr(hInterrupt), 0, 0) // Send interrupt event
    <-cancel_done_comm

    syscall.Close(hConsoleIn)
    syscall.Close(hInterrupt)
    isOpen = false
}

func GetKey() (ch rune, key Key, err error) {
    if (!isOpen) {
        panic("function GetKey() should be called after Open()")
    }

    select {
    case ev := <-input_comm:
        return ev.rune, ev.key, ev.err

    case <-interrupt_comm:
        return
    }
}

func GetSingleKey() (ch rune, key Key, err error) {
    err = Open()
    if err == nil {
        ch, key, err = GetKey()
        Close()
    }
    return
}