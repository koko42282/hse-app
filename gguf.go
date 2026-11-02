package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// readGGUFInfo reads only the GGUF header and returns general.architecture and
// general.name (these keys come first in practically every file). Best-effort.
func readGGUFInfo(path string) (arch, name string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var magic [4]byte
	if _, err = io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return "", "", fmt.Errorf("not a GGUF file")
	}
	var ver uint32
	var tensors, kvs uint64
	if err = binary.Read(r, binary.LittleEndian, &ver); err != nil || ver < 2 || ver > 3 {
		return "", "", fmt.Errorf("unsupported GGUF version %d", ver)
	}
	if err = binary.Read(r, binary.LittleEndian, &tensors); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &kvs); err != nil {
		return
	}
	if kvs > 4096 {
		kvs = 4096
	}
	readStr := func(keep bool) (string, error) {
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return "", err
		}
		if !keep || n > 4096 {
			_, err := r.Discard(int(min64(n, 1<<31)))
			return "", err
		}
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return string(b), err
	}
	var skip func(t uint32) error
	skip = func(t uint32) error {
		sizes := map[uint32]int{0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 6: 4, 7: 1, 10: 8, 11: 8, 12: 8}
		if n, ok := sizes[t]; ok {
			_, err := r.Discard(n)
			return err
		}
		switch t {
		case 8:
			_, err := readStr(false)
			return err
		case 9:
			var et uint32
			var n uint64
			if err := binary.Read(r, binary.LittleEndian, &et); err != nil {
				return err
			}
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				return err
			}
			for i := uint64(0); i < n; i++ {
				if err := skip(et); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf("unknown type %d", t)
	}
	for i := uint64(0); i < kvs && (arch == "" || name == ""); i++ {
		key, e := readStr(true)
		if e != nil {
			return arch, name, e
		}
		var t uint32
		if e = binary.Read(r, binary.LittleEndian, &t); e != nil {
			return arch, name, e
		}
		if t == 8 && (key == "general.architecture" || key == "general.name") {
			v, e := readStr(true)
			if e != nil {
				return arch, name, e
			}
			if key == "general.architecture" {
				arch = v
			} else {
				name = v
			}
			continue
		}
		if e = skip(t); e != nil {
			return arch, name, e
		}
	}
	return arch, name, nil
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
