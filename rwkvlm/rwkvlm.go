// Copyright 2023 NLP Odyssey Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rwkvlm

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nlpodyssey/rwkv"
	"github.com/nlpodyssey/spago/ag"
	"github.com/nlpodyssey/spago/embeddings"
	"github.com/nlpodyssey/spago/embeddings/store"
	"github.com/nlpodyssey/spago/embeddings/store/diskstore"
	"github.com/nlpodyssey/spago/mat"
	"github.com/nlpodyssey/spago/mat/float"
	"github.com/nlpodyssey/spago/nn"
	"github.com/nlpodyssey/spago/nn/normalization/layernorm"
)

type Model struct {
	nn.Module
	Embeddings *Embeddings
	Encoder    *rwkv.Model
	LN         *layernorm.Model
	Linear     nn.Param `spago:"type:weights"`
	Config     Config
}

type Config struct {
	DModel              int
	NumHiddenLayers     int
	RescaleLayer        int
	VocabSize           int
	EmbeddingsStoreName string
}

func init() {
	gob.Register(&Model{})
}

func New[T float.DType](c Config, repo store.Repository) *Model {
	return &Model{
		Config: c,
		Encoder: rwkv.New[T](rwkv.Config{
			DModel:       c.DModel,
			NumLayers:    c.NumHiddenLayers,
			RescaleLayer: c.RescaleLayer,
		}),
		LN:         layernorm.New[T](c.DModel, 1e-6),
		Linear:     nn.NewParam(mat.NewEmptyDense[T](c.VocabSize, c.DModel)),
		Embeddings: NewEmbeddings[T](c, repo),
	}
}

// Load loads a pre-trained model from the given path.
func Load(dir string, filename string) (*Model, error) {
	m, err := loadFromFile(filepath.Join(dir, filename))
	if err != nil {
		panic(err)
	}
	embeddingsRepo, err := diskstore.NewRepository(filepath.Join(dir, "repo"), diskstore.ReadOnlyMode)
	if err != nil {
		return nil, fmt.Errorf("failed to load embeddings repository: %w", err)
	}
	err = m.applyEmbeddings(embeddingsRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to apply embeddings: %w", err)
	}
	return m, nil
}

// Dump saves the Model to a file.
// See gobEncode for further details.
func Dump(obj *Model, filename string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer func() {
		if e := f.Close(); e != nil && err == nil {
			err = e
		}
	}()
	if err = gobEncode(obj, f); err != nil {
		return err
	}
	return nil
}

// applyEmbeddings sets the embeddings of the model.
func (m *Model) applyEmbeddings(repo *diskstore.Repository) (err error) {
	nn.Apply(m, func(model nn.Model, name string) {
		switch em := model.(type) {
		case *embeddings.Model[[]byte], *embeddings.Model[int], *embeddings.Model[string]:
			if e := em.(interface {
				UseRepository(repo store.Repository) error
			}).UseRepository(repo); e != nil && err == nil {
				err = e
			}
		}
	})
	return err
}

// Encode returns the encoding of the given input considering the last state.
func (m *Model) Encode(context []int, s rwkv.State, encodeFullSequence bool) (ag.Node, rwkv.State) {
	if encodeFullSequence {
		// transform the context into a sequence of embeddings
		encoded := m.Embeddings.Encode(context)
		var x ag.Node
		for _, e := range encoded {
			x, s = m.Encoder.Forward(e, s)
		}
		return x, s
	}

	// encode only the last token
	x := m.Embeddings.Encode(context[len(context)-1:])[0]
	return m.Encoder.Forward(x, s)
}

// Predict returns the prediction logits of the next token.
func (m *Model) Predict(x ag.Node) ag.Node {
	return ag.Mul(m.Linear, m.LN.Forward(x)[0])
}
