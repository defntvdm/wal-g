package walg

import (
	"archive/tar"
	"github.com/pkg/errors"
	"io"
)

// EmptyWriteIgnorer handles 0 byte write in LZ4 package
// to stop pipe reader/writer from blocking.
type EmptyWriteIgnorer struct {
	io.WriteCloser
}

func (e EmptyWriteIgnorer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return e.WriteCloser.Write(p)
}

// Extract exactly one tar bundle. Returns an error
// upon failure. Able to configure behavior by passing
// in different TarInterpreters.
func extractOne(ti TarInterpreter, s io.Reader) error {
	tr := tar.NewReader(s)

	for {
		cur, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Wrap(err, "extractOne: tar extract failed")
		}

		err = ti.Interpret(tr, cur)
		if err != nil {
			return errors.Wrap(err, "extractOne: Interpret failed")
		}
	}
	return nil

}

// Ensures that file extension is valid. Any subsequent behavior
// depends on file type.
func tarHandler(wc io.WriteCloser, rm ReaderMaker) error {
	defer wc.Close()
	r, err := rm.Reader()
	if err != nil {
		return errors.Wrap(err, "ExtractAll: failed to create new reader")
	}
	defer r.Close()

	if rm.Format() == "lzo" {
		err = DecompressLzo(wc, r)
		if err != nil {
			return errors.Wrap(err, "ExtractAll: lzo decompress failed")
		}
	} else if rm.Format() == "lz4" {
		err = DecompressLz4(wc, r)
		if err != nil {
			return errors.Wrap(err, "ExtractAll: lz4 decompress failed")
		}
	} else if rm.Format() == "tar" {
		_, err = io.Copy(wc, r)
		if err != nil {
			return errors.Wrap(err, "ExtractAll: tar extract failed")
		}
	} else if rm.Format() == "nop" {
	} else {
		return errors.Wrap(UnsupportedFileTypeError{rm.Path(), rm.Format()}, "ExtractAll:")
	}
	return nil
}

// ExtractAll Handles all files passed in. Supports `.lzo`, `.lz4, and `.tar`.
// File type `.nop` is used for testing purposes. Each file is extracted
// in its own goroutine and ExtractAll will wait for all goroutines to finish.
// Returns the first error encountered.
func ExtractAll(ti TarInterpreter, files []ReaderMaker) error {
	if len(files) < 1 {
		return errors.New("ExtractAll: did not provide files to extract")
	}

	sem := make(chan Empty, len(files))
	collectAll := make(chan error)
	defer close(collectAll)

	for i, val := range files {
		go func(i int, val ReaderMaker) {
			pr, tempW := io.Pipe()
			pw := &EmptyWriteIgnorer{tempW}

			//Collect errors returned by tarHandler.
			collectLow := make(chan error)
			go func() {
				collectLow <- tarHandler(pw, val)
			}()

			//Collect errors returned by extractOne.
			collectTop := make(chan error)
			go func() {
				defer pr.Close()
				err := extractOne(ti, pr)
				collectTop <- err
			}()

			finishedTop := false
			finishedLow := false

			for !(finishedTop && finishedLow) {
				select {
				case err := <-collectTop:
					finishedTop = true
					collectAll <- err
				case err := <-collectLow:
					finishedLow = true
					collectAll <- err
				}
			}

			sem <- Empty{}
		}(i, val)
	}

	var err error
	go func() {
		for e := range collectAll {
			if e != nil {
				err = e
			}
		}
	}()

	for i := 0; i < len(files); i++ {
		<-sem
	}
	return err
}