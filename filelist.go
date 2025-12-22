package main

import (
	"archive/zip"
	"bufio"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"servbot/walker"
	"strconv"
	"strings"
	"time"
)

const FILELIST_PATH = "filelists/list"
const USERLIST_PATH = "filelists/userlist.zip"

type ListWriter struct {
	fp              *os.File
	zw              *zip.Writer
	zfp             io.Writer
	zfpbw           *bufio.Writer
	dirsWritten     int
	paths           []string
	Filename        string
	removedPrefixes []string
}

func (w *ListWriter) Close() {
	w.zfpbw.Flush()
	w.zw.Close()
	w.fp.Close()
}

func (w *ListWriter) Write(b []byte) (n int, err error) {
	return w.zfpbw.Write(b)
}

func (w *ListWriter) WriteFile(path string, info os.FileInfo, hash string, prefix string) {
	if !w.inList(path) {
		return
	}
	if info.IsDir() {
		if w.dirsWritten > 0 {
			w.Write([]byte("\n"))
		}
		cleanedPath := path
		for _, p := range w.removedPrefixes {
			var cut bool
			cleanedPath, cut = strings.CutPrefix(cleanedPath, p)
			if cut {
				break
			}
		}
		w.Write([]byte(cleanedPath + "\n"))
		w.Write([]byte(strings.Repeat("=", 80) + "\n"))
		w.dirsWritten++
		return
	}
	w.Write([]byte("!" + prefix + " " + hash + " | " + filepath.Base(path) +
		" ::INFO:: " + humanSize(info.Size()) + "\n"))

}

func (w *ListWriter) inList(path string) bool {
	for _, prefix := range w.paths {
		if path == prefix {
			return true
		}
		if strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func NewListWriter(filename string, zfilename string, paths []string, removedPrefixes []string) (*ListWriter, error) {
	// In case a symlink is passed, filepath.walk doesn't walk them.
	newPaths := append([]string{}, paths...)
	for i := range newPaths {
		newPaths[i] = strings.TrimSuffix(newPaths[i], "/")
	}
	w := &ListWriter{paths: newPaths, Filename: filename, removedPrefixes: removedPrefixes}
	fp, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	w.fp = fp
	w.zw = zip.NewWriter(w.fp)
	fh := zip.FileHeader{Name: zfilename, Modified: time.Now().UTC(),
		Method: zip.Deflate}
	zfp, err := w.zw.CreateHeader(&fh)
	if err != nil {
		w.zw.Close()
		fp.Close()
		return nil, err
	}
	w.zfp = zfp
	w.zfpbw = bufio.NewWriter(w.zfp)
	return w, nil
}

func makeFilelist(cfg Config) {
	err := os.Mkdir("filelists", 0777)
	if err != nil && !os.IsExist(err) {
		log.Fatalf("Unable to create filelists directory: %s", err)
	}
	log.Printf("Generating list from paths %v", cfg.Listpaths)
	fp, err := os.Create(FILELIST_PATH + ".tmp")
	if err != nil {
		panic(err)
	}
	bw := bufio.NewWriter(fp)
	defer bw.Flush()
	date := time.Now().Format(time.DateOnly)
	fn := cfg.Nick + "_" + date + ".txt"

	userFp, err := NewListWriter("filelists/userlist.tmp", fn, cfg.Listpaths, cfg.RemovedPrefixes)
	if err != nil {
		log.Fatal("Creating userlist:", err)
	}
	defer userFp.Close()

	var lists []*ListWriter
	lists = append(lists, userFp)
	for _, l := range cfg.Lists {
		fn := cfg.Nick + "_" + l.Name + "_" + date + ".txt"
		writer, err := NewListWriter("filelists/"+l.Name+".tmp", fn, l.Paths, cfg.RemovedPrefixes)
		if err != nil {
			panic(err)
		}
		lists = append(lists, writer)
	}
	for _, l := range lists {
		headerFp, err := os.Open("header.txt")
		if err == nil {
			io.Copy(l, headerFp)
			headerFp.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Fatalf("Error opening header: %v", err)
		}
	}
	var totalSize int64

	for _, basepath := range cfg.Listpaths {
		log.Println("Walking", basepath)
		err := walker.WalkDir(basepath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fmt.Printf("Ignoring %s: %v", path, err)
				return err
			}
			if strings.Contains(path, "\t") {
				log.Printf("Skipping %s because of embedded tab", path)
				return nil
			}
			if strings.Contains(path, "\n") {
				log.Printf("Skipping %s because of embedded newline", path)
				return nil
			}
			hash := fmt.Sprintf("%x", md5.Sum([]byte(path)))
			hash = hash[:12]
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() {
				bw.Write([]byte(hash + "\t" + path + "\t" + strconv.FormatInt(info.Size(), 10) + "\n"))
			}
			for _, l := range lists {
				l.WriteFile(path, info, hash, cfg.Prefix)
			}
			totalSize += info.Size()
			return nil
		})
		if err != nil {
			panic(err)
		}
	}
	for _, l := range lists {
		l.Close()
		newName := strings.TrimSuffix(l.Filename, ".tmp") + ".zip"
		os.Rename(l.Filename, newName)
	}

}

func findFile(listFile string, name string) string {
	var hash string
	var filename string
	if strings.Contains(name, " | ") {
		a := strings.Split(name, " | ")
		hash = a[0]
		filename = a[1]
	} else {
		filename = name
	}

	fp, err := os.Open(listFile)
	if err != nil {
		panic(err)
	}
	scanner := bufio.NewScanner(fp)
	for scanner.Scan() {
		items := strings.Split(scanner.Text(), "\t")
		if hash != "" && items[0] == hash {
			return items[1]
		} else if hash == "" && filepath.Base(items[1]) == filename {
			return items[1]
		}
	}
	return ""
}

var bases = []string{"B", "KB", "MB", "GB", "TB"}

func humanSize(s int64) string {
	if s < 1024 {
		return fmt.Sprintf("%dB", s)
	}
	sf := float64(s)
	i := 0
	for sf >= 1024 && i < len(bases) {
		sf = sf / 1024
		i++
	}
	return fmt.Sprintf("%.2f%s", sf, bases[i])
}
