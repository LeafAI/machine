package main

import (
 "syscall"
 "unsafe"
)

func isTerminal(fd uintptr)bool{
 var t syscall.Termios
 _,_,err:=syscall.Syscall(syscall.SYS_IOCTL,fd,uintptr(syscall.TIOCGETA),uintptr(unsafe.Pointer(&t)))
 return err==0
}
