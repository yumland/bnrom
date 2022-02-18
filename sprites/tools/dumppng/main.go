package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"os"
	"runtime"

	"github.com/nbarena/bnrom/sprites"
	"github.com/nbarena/pngchunks"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

func FindBbox(img image.Image) image.Rectangle {
	left := img.Bounds().Min.X
	top := img.Bounds().Min.Y
	right := img.Bounds().Max.X
	bottom := img.Bounds().Max.Y

	for left = img.Bounds().Min.X; left < img.Bounds().Max.X; left++ {
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			_, _, _, a := img.At(left, y).RGBA()
			if a != 0 {
				goto leftDone
			}
		}
		continue
	leftDone:
		break
	}

	for top = img.Bounds().Min.Y; top < img.Bounds().Max.Y; top++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			_, _, _, a := img.At(x, top).RGBA()
			if a != 0 {
				goto topDone
			}
		}
		continue
	topDone:
		break
	}

	for right = img.Bounds().Max.X - 1; right >= img.Bounds().Min.X; right-- {
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			_, _, _, a := img.At(right, y).RGBA()
			if a != 0 {
				goto rightDone
			}
		}
		continue
	rightDone:
		break
	}
	right++

	for bottom = img.Bounds().Max.Y - 1; bottom >= img.Bounds().Min.Y; bottom-- {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			_, _, _, a := img.At(x, bottom).RGBA()
			if a != 0 {
				goto bottomDone
			}
		}
		continue
	bottomDone:
		break
	}
	bottom++

	if right < left || bottom < top {
		return image.Rect(0, 0, 0, 0)
	}

	return image.Rectangle{image.Point{left, top}, image.Point{right, bottom}}
}

type FrameInfo struct {
	BBox   image.Rectangle
	Origin image.Point
	Delay  int
	Action sprites.FrameAction
}

func processOne(idx int, anims []sprites.Animation) error {
	left := 0
	top := 0

	var infos []FrameInfo
	var fullPalette color.Palette
	spriteImg := image.NewPaletted(image.Rect(0, 0, 1024, 1024), nil)

	for _, anim := range anims {
		for _, frame := range anim.Frames {
			fullPalette = frame.Palette

			var frameInfo FrameInfo
			frameInfo.Delay = int(frame.Delay)
			frameInfo.Action = frame.Action

			img := frame.MakeImage()
			spriteImg.Palette = img.Palette

			trimBbox := FindBbox(img)

			frameInfo.Origin.X = img.Rect.Dx()/2 - trimBbox.Min.X
			frameInfo.Origin.Y = img.Rect.Dy()/2 - trimBbox.Min.Y

			if left+trimBbox.Dx() > spriteImg.Rect.Dx() {
				left = 0
				top = FindBbox(spriteImg).Max.Y
				top++
			}

			frameInfo.BBox = image.Rectangle{image.Point{left, top}, image.Point{left + trimBbox.Dx(), top + trimBbox.Dy()}}

			draw.Draw(spriteImg, frameInfo.BBox, img, trimBbox.Min, draw.Over)
			infos = append(infos, frameInfo)

			left += trimBbox.Dx() + 1
		}
	}

	subimg := spriteImg.SubImage(FindBbox(spriteImg))
	if subimg.Bounds().Dx() == 0 || subimg.Bounds().Dy() == 0 {
		return nil
	}

	r, w := io.Pipe()

	var g errgroup.Group

	g.Go(func() error {
		defer w.Close()
		if err := png.Encode(w, subimg); err != nil {
			return err
		}
		return nil
	})

	f, err := os.Create(fmt.Sprintf("sprites/%04d.png", idx))
	if err != nil {
		return err
	}
	defer f.Close()

	pngr, err := pngchunks.NewReader(r)
	if err != nil {
		return err
	}

	pngw, err := pngchunks.NewWriter(f)
	if err != nil {
		return err
	}

	for {
		chunk, err := pngr.NextChunk()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
		}

		if err := pngw.WriteChunk(chunk.Length(), chunk.Type(), chunk); err != nil {
			return err
		}

		if chunk.Type() == "tRNS" {
			// Pack metadata in here.
			{
				var buf bytes.Buffer
				buf.WriteString("full")
				buf.WriteByte('\x00')
				buf.WriteByte('\x08')
				for _, c := range fullPalette {
					rgba := c.(color.RGBA)
					buf.WriteByte(rgba.R)
					buf.WriteByte(rgba.G)
					buf.WriteByte(rgba.B)
					buf.WriteByte(rgba.A)
					buf.WriteByte('\xff')
					buf.WriteByte('\xff')
				}
				if err := pngw.WriteChunk(int32(buf.Len()), "sPLT", bytes.NewBuffer(buf.Bytes())); err != nil {
					return err
				}
			}

			{
				var buf bytes.Buffer
				buf.WriteString("fsctrl")
				buf.WriteByte('\x00')
				buf.WriteByte('\xff')
				for _, info := range infos {
					binary.Write(&buf, binary.LittleEndian, int16(info.BBox.Min.X))
					binary.Write(&buf, binary.LittleEndian, int16(info.BBox.Min.Y))
					binary.Write(&buf, binary.LittleEndian, int16(info.BBox.Max.X))
					binary.Write(&buf, binary.LittleEndian, int16(info.BBox.Max.Y))
					binary.Write(&buf, binary.LittleEndian, int16(info.Origin.X))
					binary.Write(&buf, binary.LittleEndian, int16(info.Origin.Y))
					buf.WriteByte(uint8(info.Delay))
					buf.WriteByte(uint8(info.Action))
				}
				if err := pngw.WriteChunk(int32(buf.Len()), "zTXt", bytes.NewBuffer(buf.Bytes())); err != nil {
					return err
				}

			}
		}

		if err := chunk.Close(); err != nil {
			return err
		}
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

func main() {
	f, err := os.Open("BN6 Gregar.gba")
	if err != nil {
		log.Fatalf("%s", err)
	}

	buf, err := ioutil.ReadAll(f)
	if err != nil {
		log.Fatalf("%s", err)
	}

	r := bytes.NewReader(buf)

	if _, err := r.Seek(0x00031CEC, os.SEEK_SET); err != nil {
		log.Fatalf("%s", err)
	}
	s, err := sprites.Read(r, 815)
	if err != nil {
		log.Fatalf("%s", err)
	}

	os.Mkdir("sprites", 0o700)

	bar := progressbar.Default(int64(len(s)))
	type work struct {
		idx   int
		anims []sprites.Animation
	}

	ch := make(chan work, runtime.NumCPU())

	var g errgroup.Group
	for i := 0; i < runtime.NumCPU(); i++ {
		g.Go(func() error {
			for w := range ch {
				bar.Add(1)
				if err := processOne(w.idx, w.anims); err != nil {
					return err
				}
			}
			return nil
		})
	}

	for spriteIdx, anims := range s {
		ch <- work{spriteIdx, anims}
	}
	close(ch)

	if err := g.Wait(); err != nil {
		log.Fatalf("%s", err)
	}
}
