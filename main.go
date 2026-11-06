//go:build windows

// camfocus: turn webcam autofocus on/off on Windows via the standard
// DirectShow IAMCameraControl interface. No drivers, no install, no admin,
// standard library only.
//
// Usage:
//
//	camfocus list                         show cameras and their focus state
//	camfocus off [-camera C200] [-focus N] disable autofocus (keeps current focus unless -focus given)
//	camfocus on  [-camera C200]           re-enable autofocus
//
// -camera is a case-insensitive substring of the camera name (default "C200");
// pass -camera "" to target every camera.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

type guid struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

var (
	clsidSystemDeviceEnum         = guid{0x62BE5D10, 0x60EB, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	clsidVideoInputDeviceCategory = guid{0x860BB310, 0x5D01, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidICreateDevEnum             = guid{0x29840822, 0x5B84, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidIBaseFilter                = guid{0x56A86895, 0x0AD4, 0x11CE, [8]byte{0xB0, 0x3A, 0x00, 0x20, 0xAF, 0x0B, 0xA7, 0x70}}
	iidIAMCameraControl           = guid{0xC6E13370, 0x30AC, 0x11D0, [8]byte{0xA1, 0x8C, 0x00, 0xA0, 0xC9, 0x11, 0x89, 0x56}}
	iidIPropertyBag               = guid{0x55272A00, 0x42CB, 0x11CE, [8]byte{0x81, 0x35, 0x00, 0xAA, 0x00, 0x4B, 0xB8, 0x51}}
)

const (
	clsctxInprocServer      = 0x1
	coinitApartmentThreaded = 0x2
	vtBSTR                  = 8

	cameraControlFocus = 6
	flagAuto           = 0x1
	flagManual         = 0x2
)

// vtable indices
const (
	vtQueryInterface = 0
	vtRelease        = 2

	createDevEnumCreateClassEnumerator = 3
	enumMonikerNext                    = 3
	monikerBindToObject                = 8
	monikerBindToStorage               = 9
	propertyBagRead                    = 3
	camCtrlGetRange                    = 3
	camCtrlSet                         = 4
	camCtrlGet                         = 5
)

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	oleaut32             = syscall.NewLazyDLL("oleaut32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procVariantClear     = oleaut32.NewProc("VariantClear")
)

// com is any COM interface pointer: its first word points at the vtable.
type com struct{ vtbl *[32]uintptr }

func (o *com) ptr() uintptr     { return uintptr(unsafe.Pointer(o)) }
func (o *com) fn(i int) uintptr { return o.vtbl[i] }
func (o *com) release() {
	if o != nil {
		syscall.SyscallN(o.fn(vtRelease), o.ptr())
	}
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

type variant struct {
	vt  uint16
	_   [3]uint16
	val unsafe.Pointer
	_   uintptr
}

type camera struct {
	name string
	ctrl *com // IAMCameraControl, nil if unsupported
}

type focusState struct {
	min, max, step, def, caps int32
	value, flags              int32
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	match := fs.String("camera", "C200", `case-insensitive substring of the camera name ("" = all cameras)`)
	focus := fs.Int("focus", 0, "fixed focus value to set with 'off' (default: keep current position)")
	fs.Parse(os.Args[2:])
	focusGiven := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "focus" {
			focusGiven = true
		}
	})

	runtime.LockOSThread()
	if hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); failed(hr) {
		fatal("CoInitializeEx failed: 0x%08X", uint32(hr))
	}
	defer procCoUninitialize.Call()

	cams, err := listCameras()
	if err != nil {
		fatal("%v", err)
	}
	defer func() {
		for _, c := range cams {
			c.ctrl.release()
		}
	}()

	switch cmd {
	case "list":
		if len(cams) == 0 {
			fmt.Println("No cameras found.")
		}
		for i, c := range cams {
			fmt.Printf("[%d] %s\n    %s\n", i, c.name, describe(c))
		}
	case "off", "on":
		hit := 0
		for _, c := range cams {
			if !strings.Contains(strings.ToLower(c.name), strings.ToLower(*match)) {
				continue
			}
			hit++
			if err := apply(c, cmd == "off", focusGiven, int32(*focus)); err != nil {
				fmt.Printf("%s: %v\n", c.name, err)
				continue
			}
			fmt.Printf("%s: %s\n", c.name, describe(c))
		}
		if hit == 0 {
			fatal("no camera matching %q; run 'camfocus list' to see names", *match)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  camfocus list
  camfocus off [-camera C200] [-focus N]
  camfocus on  [-camera C200]`)
	os.Exit(2)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "camfocus: "+format+"\n", a...)
	os.Exit(1)
}

func listCameras() ([]camera, error) {
	var devEnum *com
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidSystemDeviceEnum)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidICreateDevEnum)), uintptr(unsafe.Pointer(&devEnum)))
	if failed(hr) || devEnum == nil {
		return nil, fmt.Errorf("cannot create SystemDeviceEnum: 0x%08X", uint32(hr))
	}
	defer devEnum.release()

	var enumMon *com
	hr, _, _ = syscall.SyscallN(devEnum.fn(createDevEnumCreateClassEnumerator), devEnum.ptr(),
		uintptr(unsafe.Pointer(&clsidVideoInputDeviceCategory)), uintptr(unsafe.Pointer(&enumMon)), 0)
	if failed(hr) {
		return nil, fmt.Errorf("cannot enumerate video devices: 0x%08X", uint32(hr))
	}
	if enumMon == nil { // S_FALSE: category is empty
		return nil, nil
	}
	defer enumMon.release()

	var cams []camera
	for {
		var mon *com
		var fetched uint32
		hr, _, _ = syscall.SyscallN(enumMon.fn(enumMonikerNext), enumMon.ptr(), 1,
			uintptr(unsafe.Pointer(&mon)), uintptr(unsafe.Pointer(&fetched)))
		if hr != 0 || fetched == 0 || mon == nil {
			break
		}
		cam := camera{name: friendlyName(mon)}

		var filter *com
		hr, _, _ = syscall.SyscallN(mon.fn(monikerBindToObject), mon.ptr(), 0, 0,
			uintptr(unsafe.Pointer(&iidIBaseFilter)), uintptr(unsafe.Pointer(&filter)))
		if !failed(hr) && filter != nil {
			var ctrl *com
			hr, _, _ = syscall.SyscallN(filter.fn(vtQueryInterface), filter.ptr(),
				uintptr(unsafe.Pointer(&iidIAMCameraControl)), uintptr(unsafe.Pointer(&ctrl)))
			if !failed(hr) {
				cam.ctrl = ctrl
			}
			filter.release()
		}
		mon.release()
		cams = append(cams, cam)
	}
	return cams, nil
}

func friendlyName(mon *com) string {
	var bag *com
	hr, _, _ := syscall.SyscallN(mon.fn(monikerBindToStorage), mon.ptr(), 0, 0,
		uintptr(unsafe.Pointer(&iidIPropertyBag)), uintptr(unsafe.Pointer(&bag)))
	if failed(hr) || bag == nil {
		return "(unknown)"
	}
	defer bag.release()

	prop, _ := syscall.UTF16PtrFromString("FriendlyName")
	var v variant
	hr, _, _ = syscall.SyscallN(bag.fn(propertyBagRead), bag.ptr(),
		uintptr(unsafe.Pointer(prop)), uintptr(unsafe.Pointer(&v)), 0)
	if failed(hr) {
		return "(unknown)"
	}
	defer procVariantClear.Call(uintptr(unsafe.Pointer(&v)))
	if v.vt != vtBSTR || v.val == nil {
		return "(unknown)"
	}
	p := (*uint16)(v.val)
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

func getFocus(ctrl *com) (focusState, error) {
	var f focusState
	if ctrl == nil {
		return f, fmt.Errorf("camera exposes no camera controls")
	}
	hr, _, _ := syscall.SyscallN(ctrl.fn(camCtrlGetRange), ctrl.ptr(), cameraControlFocus,
		uintptr(unsafe.Pointer(&f.min)), uintptr(unsafe.Pointer(&f.max)), uintptr(unsafe.Pointer(&f.step)),
		uintptr(unsafe.Pointer(&f.def)), uintptr(unsafe.Pointer(&f.caps)))
	if failed(hr) {
		return f, fmt.Errorf("no focus control (0x%08X)", uint32(hr))
	}
	hr, _, _ = syscall.SyscallN(ctrl.fn(camCtrlGet), ctrl.ptr(), cameraControlFocus,
		uintptr(unsafe.Pointer(&f.value)), uintptr(unsafe.Pointer(&f.flags)))
	if failed(hr) {
		return f, fmt.Errorf("cannot read focus (0x%08X)", uint32(hr))
	}
	return f, nil
}

func setFocus(ctrl *com, value, flags int32) error {
	hr, _, _ := syscall.SyscallN(ctrl.fn(camCtrlSet), ctrl.ptr(), cameraControlFocus,
		uintptr(value), uintptr(flags))
	if failed(hr) {
		return fmt.Errorf("setting focus failed (0x%08X)", uint32(hr))
	}
	return nil
}

func apply(c camera, disableAuto, focusGiven bool, focus int32) error {
	f, err := getFocus(c.ctrl)
	if err != nil {
		return err
	}
	if !disableAuto {
		return setFocus(c.ctrl, f.value, flagAuto)
	}
	if f.caps&flagManual == 0 {
		return fmt.Errorf("camera does not support manual focus")
	}
	v := f.value
	if focusGiven {
		v = focus
	}
	if v < f.min {
		v = f.min
	}
	if v > f.max {
		v = f.max
	}
	return setFocus(c.ctrl, v, flagManual)
}

func describe(c camera) string {
	f, err := getFocus(c.ctrl)
	if err != nil {
		return err.Error()
	}
	mode := "manual"
	if f.flags&flagAuto != 0 {
		mode = "AUTO"
	}
	return fmt.Sprintf("focus: %s, value %d (range %d..%d, step %d, default %d)",
		mode, f.value, f.min, f.max, f.step, f.def)
}
