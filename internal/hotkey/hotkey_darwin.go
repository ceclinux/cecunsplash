//go:build darwin

package hotkey

/*
#cgo darwin LDFLAGS: -framework Carbon -framework CoreFoundation
#include <Carbon/Carbon.h>

extern void goHotKeyPressed();

static EventHotKeyRef gHotKeyRef = NULL;
static EventHandlerRef gHandlerRef = NULL;
static volatile int gStop = 0;

static OSStatus hotKeyHandler(EventHandlerCallRef nextHandler, EventRef event, void *userData) {
    EventHotKeyID hkID;
    OSStatus err = GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hkID), NULL, &hkID);
    if (err == noErr && hkID.signature == 'cuns' && hkID.id == 1) {
        goHotKeyPressed();
    }
    return noErr;
}

static OSStatus registerHotKey(UInt32 keyCode, UInt32 modifiers) {
    EventTypeSpec eventType;
    eventType.eventClass = kEventClassKeyboard;
    eventType.eventKind = kEventHotKeyPressed;

    if (gHandlerRef == NULL) {
        OSStatus err = InstallApplicationEventHandler(&hotKeyHandler, 1, &eventType, NULL, &gHandlerRef);
        if (err != noErr) {
            return err;
        }
    }

    EventHotKeyID hotKeyID;
    hotKeyID.signature = 'cuns';
    hotKeyID.id = 1;

    return RegisterEventHotKey(keyCode, modifiers, hotKeyID, GetApplicationEventTarget(), 0, &gHotKeyRef);
}

static void runHotKeyLoop() {
    gStop = 0;
    while (!gStop) {
        EventRef event = NULL;
        OSStatus err = ReceiveNextEvent(0, NULL, 1.0, true, &event);
        if (err == noErr && event != NULL) {
            SendEventToEventTarget(event, GetEventDispatcherTarget());
            ReleaseEvent(event);
        }
    }
}

static void stopHotKeyLoop() {
    gStop = 1;
    if (gHotKeyRef != NULL) {
        UnregisterEventHotKey(gHotKeyRef);
        gHotKeyRef = NULL;
    }
}
*/
import "C"

import (
	"fmt"
	"strings"
)

var sink chan<- struct{}

type parsedShortcut struct {
	keyCode   C.UInt32
	modifiers C.UInt32
}

func Register(ch chan<- struct{}, shortcut string) error {
	parsed, err := parseShortcut(shortcut)
	if err != nil {
		return err
	}
	sink = ch
	if code := C.registerHotKey(parsed.keyCode, parsed.modifiers); code != 0 {
		return fmt.Errorf("register %s hotkey failed: OSStatus %d", Normalize(shortcut), int(code))
	}
	go C.runHotKeyLoop()
	return nil
}

func Stop() {
	C.stopHotKeyLoop()
}

func Normalize(shortcut string) string {
	parsed, err := splitShortcut(shortcut)
	if err != nil {
		return strings.TrimSpace(shortcut)
	}
	return strings.Join(parsed, "+")
}

func parseShortcut(shortcut string) (parsedShortcut, error) {
	parts, err := splitShortcut(shortcut)
	if err != nil {
		return parsedShortcut{}, err
	}
	var modifiers C.UInt32
	key := ""
	for _, part := range parts {
		switch part {
		case "cmd", "command", "meta":
			modifiers |= C.cmdKey
		case "ctrl", "control":
			modifiers |= C.controlKey
		case "shift":
			modifiers |= C.shiftKey
		case "alt", "option":
			modifiers |= C.optionKey
		default:
			if key != "" {
				return parsedShortcut{}, fmt.Errorf("shortcut %q has multiple keys (%q and %q)", shortcut, key, part)
			}
			key = part
		}
	}
	if key == "" {
		return parsedShortcut{}, fmt.Errorf("shortcut %q has no key", shortcut)
	}
	if modifiers == 0 {
		return parsedShortcut{}, fmt.Errorf("shortcut %q must include at least one modifier", shortcut)
	}
	keyCode, ok := keyCodes[key]
	if !ok {
		return parsedShortcut{}, fmt.Errorf("unsupported shortcut key %q", key)
	}
	return parsedShortcut{keyCode: C.UInt32(keyCode), modifiers: modifiers}, nil
}

func splitShortcut(shortcut string) ([]string, error) {
	shortcut = strings.ToLower(strings.TrimSpace(shortcut))
	shortcut = strings.ReplaceAll(shortcut, " ", "")
	shortcut = strings.ReplaceAll(shortcut, "-", "+")
	parts := strings.Split(shortcut, "+")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("empty shortcut")
	}
	return cleaned, nil
}

var keyCodes = map[string]int{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7,
	"c": 8, "v": 9, "b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16,
	"t": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24,
	"equal": 24, "9": 25, "7": 26, "-": 27, "minus": 27, "8": 28, "0": 29,
	"]": 30, "rightbracket": 30, "o": 31, "u": 32, "[": 33, "leftbracket": 33,
	"i": 34, "p": 35, "l": 37, "j": 38, "'": 39, "quote": 39, "k": 40, ";": 41,
	"semicolon": 41, "\\": 42, "backslash": 42, ",": 43, "comma": 43, "/": 44, "slash": 44,
	"n": 45, "m": 46, ".": 47, "period": 47, "`": 50, "grave": 50,
	"space": 49, "tab": 48, "return": 36, "enter": 36, "escape": 53, "esc": 53,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
}

//export goHotKeyPressed
func goHotKeyPressed() {
	if sink == nil {
		return
	}
	select {
	case sink <- struct{}{}:
	default:
	}
}
