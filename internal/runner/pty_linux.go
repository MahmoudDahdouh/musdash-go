package runner

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// openPTY opens a new pseudo-terminal and returns its two ends: the one
// this process keeps and the one a command is given.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var number uint32
	var unlock int32
	if err = control(master, syscall.TIOCGPTN, unsafe.Pointer(&number)); err == nil {
		err = control(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	}
	if err == nil {
		slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(number), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
