package runner

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a new pseudo-terminal and returns its two ends: the one
// this process keeps and the one a command is given. macOS is where
// musdash is developed, not where it is deployed.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	name := make([]byte, 128)
	if err = control(master, syscall.TIOCPTYGRANT, nil); err == nil {
		if err = control(master, syscall.TIOCPTYUNLK, nil); err == nil {
			err = control(master, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0]))
		}
	}
	if err == nil {
		if end := bytes.IndexByte(name, 0); end > 0 {
			name = name[:end]
		}
		slave, err = os.OpenFile(string(name), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
