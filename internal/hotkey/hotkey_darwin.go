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

static OSStatus registerHotKey() {
    EventTypeSpec eventType;
    eventType.eventClass = kEventClassKeyboard;
    eventType.eventKind = kEventHotKeyPressed;

    OSStatus err = InstallApplicationEventHandler(&hotKeyHandler, 1, &eventType, NULL, &gHandlerRef);
    if (err != noErr) {
        return err;
    }

    EventHotKeyID hotKeyID;
    hotKeyID.signature = 'cuns';
    hotKeyID.id = 1;

    return RegisterEventHotKey(kVK_ANSI_D, cmdKey | controlKey | shiftKey, hotKeyID, GetApplicationEventTarget(), 0, &gHotKeyRef);
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

import "fmt"

var sink chan<- struct{}

func Register(ch chan<- struct{}) error {
	sink = ch
	if code := C.registerHotKey(); code != 0 {
		return fmt.Errorf("register Shift+Control+Command+D hotkey failed: OSStatus %d", int(code))
	}
	go C.runHotKeyLoop()
	return nil
}

func Stop() {
	C.stopHotKeyLoop()
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
