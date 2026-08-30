package vm

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// A minimal ISO9660 reader for the ROOT DIRECTORY of an answers volume.
//
// It replaces shelling to `isoinfo`, which is not a charly dependency and whose absence
// turns "what is actually on the seed?" into a question the operator cannot answer. The
// scope is deliberately tiny — a flat root directory of small files, which is exactly and
// only what an installer answers volume is — rather than a general ISO9660 implementation
// nobody asked for.
//
// It reads the PRIMARY VOLUME DESCRIPTOR's root directory record and walks that one extent.
// No Rock Ridge, no Joliet, no subdirectories: the names on an answers volume are already
// lower-case 8.3-safe (user_configuration.json is 23 chars, so ISO9660 level 2 keeps it
// verbatim), and if that ever stops being true the reader says so instead of guessing.

const (
	isoSectorSize   = 2048
	isoPVDSector    = 16 // the volume descriptor set starts at sector 16
	isoMaxDescScan  = 32 // give up after this many descriptors rather than scanning a whole image
	isoRootRecordAt = 156
)

// SeedVolumeFiles lists the file names on an answers volume, sorted.
func SeedVolumeFiles(isoPath string) ([]string, error) {
	entries, err := readSeedRoot(isoPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// SeedVolumeFile returns one file's bytes from an answers volume.
func SeedVolumeFile(isoPath, name string) ([]byte, error) {
	entries, err := readSeedRoot(isoPath)
	if err != nil {
		return nil, err
	}
	e, ok := entries[name]
	if !ok {
		have := make([]string, 0, len(entries))
		for n := range entries {
			have = append(have, n)
		}
		sort.Strings(have)
		return nil, fmt.Errorf("%s is not on the answers volume; it holds: %s", name, strings.Join(have, ", "))
	}
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	buf := make([]byte, e.size)
	if _, err := f.ReadAt(buf, int64(e.lba)*isoSectorSize); err != nil {
		return nil, fmt.Errorf("reading %s from the answers volume: %w", name, err)
	}
	return buf, nil
}

type isoEntry struct {
	lba  uint32
	size uint32
}

func readSeedRoot(isoPath string) (map[string]isoEntry, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, fmt.Errorf("opening the answers volume: %w", err)
	}
	defer f.Close() //nolint:errcheck

	// Find the primary volume descriptor (type 1, "CD001").
	sector := make([]byte, isoSectorSize)
	var pvd []byte
	for i := 0; i < isoMaxDescScan; i++ {
		if _, err := f.ReadAt(sector, int64(isoPVDSector+i)*isoSectorSize); err != nil {
			return nil, fmt.Errorf("reading volume descriptors: %w", err)
		}
		if string(sector[1:6]) != "CD001" {
			return nil, fmt.Errorf("%s is not an ISO9660 image (no CD001 signature)", isoPath)
		}
		if sector[0] == 1 { // primary volume descriptor
			pvd = append([]byte(nil), sector...)
			break
		}
		if sector[0] == 255 { // terminator
			break
		}
	}
	if pvd == nil {
		return nil, fmt.Errorf("%s has no primary volume descriptor", isoPath)
	}

	rootLBA := le32(pvd[isoRootRecordAt+2:])
	rootLen := le32(pvd[isoRootRecordAt+10:])
	if rootLen == 0 {
		return nil, fmt.Errorf("%s has an empty root directory", isoPath)
	}

	dir := make([]byte, rootLen)
	if _, err := f.ReadAt(dir, int64(rootLBA)*isoSectorSize); err != nil {
		return nil, fmt.Errorf("reading the root directory: %w", err)
	}

	out := map[string]isoEntry{}
	for off := 0; off < len(dir); {
		recLen := int(dir[off])
		if recLen == 0 {
			// padding to the end of a sector — skip to the next one
			next := ((off / isoSectorSize) + 1) * isoSectorSize
			if next <= off || next >= len(dir) {
				break
			}
			off = next
			continue
		}
		if off+recLen > len(dir) {
			break
		}
		rec := dir[off : off+recLen]
		nameLen := int(rec[32])
		if nameLen > 0 && 33+nameLen <= len(rec) {
			raw := string(rec[33 : 33+nameLen])
			// "\x00" and "\x01" are . and ..; directories carry the flag at rec[25] bit 1.
			isDir := rec[25]&0x02 != 0
			if raw != "\x00" && raw != "\x01" && !isDir {
				name := normalizeISOName(raw)
				// Rock Ridge carries the AUTHORED name; the ISO9660 field carries an
				// upper-cased, truncated approximation of it. charly's own writer emits
				// both, so preferring RR is what makes `seed ls` print the names the
				// renderer actually wrote rather than USER_CON.JSO;1.
				if rr := rockRidgeName(rec, nameLen); rr != "" {
					name = rr
				}
				out[name] = isoEntry{lba: le32(rec[2:]), size: le32(rec[10:])}
			}
		}
		off += recLen
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no files", isoPath)
	}
	return out, nil
}

// normalizeISOName strips the ISO9660 version suffix (";1") that xorriso and genisoimage
// both append, so a caller asks for the name it authored rather than the name the format
// stores.
func normalizeISOName(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	// A trailing dot on an extension-less name is an ISO9660 artefact, not part of the name.
	return strings.TrimSuffix(raw, ".")
}

func le32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// rockRidgeName reads the POSIX name from a directory record's system-use area (the SUSP
// "NM" entries), returning "" when the image carries no Rock Ridge extension.
//
// Layout: the system-use area starts after the name field, padded to an even offset. Each
// entry is [signature(2) length(1) version(1) data…]. An NM entry's first data byte is a
// flags byte; bit 0 set means the name CONTINUES in the following NM entry, which is why
// they are concatenated rather than the first one being taken.
func rockRidgeName(rec []byte, nameLen int) string {
	start := 33 + nameLen
	if nameLen%2 == 0 {
		start++ // pad byte after an even-length name
	}
	var sb strings.Builder
	for off := start; off+4 <= len(rec); {
		entLen := int(rec[off+2])
		if entLen < 4 || off+entLen > len(rec) {
			break
		}
		sig := string(rec[off : off+2])
		switch sig {
		case "NM":
			if entLen > 5 {
				flags := rec[off+4]
				// bits 1 and 2 mean "." and ".." — a name to ignore, not to append.
				if flags&0x06 == 0 {
					sb.Write(rec[off+5 : off+entLen])
				}
			}
		case "ST":
			return sb.String()
		}
		off += entLen
	}
	return sb.String()
}
