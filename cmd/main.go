// Copyright 2023 NLP Odyssey Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/nlpodyssey/spago/ag"
	"github.com/nlpodyssey/verbaflow"
	"github.com/nlpodyssey/verbaflow/decoder"
	"github.com/nlpodyssey/verbaflow/downloader"
	"github.com/nlpodyssey/verbaflow/rwkvlm"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Println("Usage: verbaflow [download model_dir] | [convert model_dir] | [inference model_dir] [debug_level]")
		return
	}

	var debugLevel zerolog.Level
	if len(args) > 2 {
		debugLevelArg := args[2]
		var err error
		debugLevel, err = zerolog.ParseLevel(debugLevelArg)
		if err != nil {
			fmt.Printf("Error: invalid debug level argument: %s\n", debugLevelArg)
			return
		}
	} else {
		debugLevel = zerolog.TraceLevel
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).Level(debugLevel)

	switch args[0] {
	case "download":
		if len(args) < 2 {
			fmt.Println("Error: missing model dir argument")
			return
		}
		modelDir := args[1]
		log.Debug().Msgf("Downloading model in dir: %s", modelDir)
		if err := download(modelDir); err != nil {
			log.Fatal().Err(err).Send()
		}
		log.Debug().Msg("Done.")
	case "convert":
		if len(args) < 2 {
			fmt.Println("Error: missing model dir argument")
			return
		}
		modelDir := args[1]
		log.Debug().Msgf("Converting model in dir: %s", modelDir)
		if err := convert(modelDir); err != nil {
			log.Fatal().Err(err).Send()
		}
		log.Debug().Msg("Done.")
	case "inference":
		if len(args) < 2 {
			fmt.Println("Error: missing model dir argument")
			return
		}
		modelDir := args[1]
		log.Debug().Msgf("Performing inference on model in dir: %s", modelDir)
		if err := inference(modelDir); err != nil {
			log.Fatal().Err(err).Send()
		}
	default:
		fmt.Println("Usage: verbaflow [download model_dir] | [convert model_dir] | [inference model_dir] [debug_level]")
	}
}

func download(path string) error {
	modelDir, modelName, err := separateModelName(path)
	if err != nil {
		log.Fatal().Err(err).Send()
	}
	return downloader.Download(modelDir, modelName, false, "")
}

func convert(modelDir string) error {
	return rwkvlm.ConvertPickledModelToRWKVLM[float32](rwkvlm.ConverterConfig{
		ModelDir:         modelDir,
		OverwriteIfExist: false,
	})
}

func inference(modelDir string) error {
	log.Debug().Msgf("Loading model...")
	vf, err := verbaflow.Load(modelDir)
	if err != nil {
		return err
	}
	defer vf.Close()

	log.Debug().Msgf("Ready.")

	opts := decoder.DecodingOptions{
		MinLen:         0,
		MaxLen:         200,
		EndTokenID:     0,
		SkipEndTokenID: true,
		Temp:           1,
		TopP:           0.8,
		TopK:           120,
		UseSampling:    true,
		EndThreshold:   1.0,
		StopSequencesIDs: [][]int{
			{187, 23433, 27},    // \nQuestion:
			{187, 50, 708, 329}, // \nQ & A:
			{187, 50, 27},       // \nQ:
		},
	}
	fn := func(text string) error {
		start := time.Now()

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer stop()

		// buffer is a channel that will receive the generated tokens
		buffer := make(decoder.ChannelBuffer, opts.MaxLen)

		done := make(chan struct{})
		go func() {
			// prints the generated tokens to stdout
			err := processBuffer(buffer, os.Stdout, done, vf.TokenByID)
			if err != nil {
				log.Error().Err(err).Send()
			}
		}()

		err = vf.Generate(ctx, text, buffer, opts)
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		log.Trace().Msgf("Inference time: %.2f seconds", time.Since(start).Seconds())

		<-done
		return nil
	}

	err = forEachInput(os.Stdin, fn)
	if err != nil {
		log.Fatal().Err(err)
	}

	return nil
}

// processBuffer prints the generated tokens to stdout.
func processBuffer(buffer decoder.ChannelBuffer, w io.StringWriter, done chan struct{}, tokenByID func(int) (string, error)) error {
	defer close(done)
	for step := range buffer {
		token, err := tokenByID(step.TokenID)
		if err != nil {
			return fmt.Errorf("failed to reconstruct text for token ID %d", step.TokenID)
		}
		_, err = w.WriteString(token)
		if err != nil {
			return err
		}
	}
	_, err := w.WriteString("\n")
	return err
}

// forEachInput calls the given callback function for each line of input.
func forEachInput(r io.Reader, callback func(text string) error) error {
	scanner := bufio.NewScanner(r)
	for promptScan(scanner) {
		text := scanner.Text()
		if text == "" {
			continue
		}
		text = strings.Replace(text, `\n`, "\n", -1)
		if err := callback(text); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}
	return nil
}

func promptScan(scanner *bufio.Scanner) bool {
	if _, err := fmt.Print("> "); err != nil {
		panic(err)
	}
	return scanner.Scan()
}

// separateModelName separate the models directory from the model name, which format is "organization/model"
func separateModelName(path string) (string, string, error) {
	dirs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(dirs) < 3 {
		return "", "", fmt.Errorf("path must have at least three levels of directories")
	}
	lastDir := dirs[len(dirs)-1]
	secondLastDir := dirs[len(dirs)-2]

	pathExceptLastTwo := strings.Join(dirs[:len(dirs)-2], "/")
	return pathExceptLastTwo, filepath.Join(secondLastDir, lastDir), nil
}

func init() {
	ag.SetDebugMode(false)
}
