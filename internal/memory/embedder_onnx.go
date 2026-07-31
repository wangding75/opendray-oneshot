//go:build local_onnx

// Build tag `local_onnx` opts into the cgo-heavy local ONNX
// embedder. Operators on plain `go build` get a binary without
// onnxruntime / libtokenizers linked, falling back to BM25 +
// HTTP backends. This keeps the default build cgo-free.
//
// To build with this embedder:
//
//	export ONNXRUNTIME_LIB=/opt/homebrew/opt/onnxruntime/lib
//	export TOKENIZERS_LIB=$HOME/.opendray/deps
//	CGO_LDFLAGS="-L$ONNXRUNTIME_LIB -L$TOKENIZERS_LIB" \
//	  go build -tags local_onnx ./cmd/opendray
//
// At runtime the dynamic linker also needs to find libonnxruntime;
// either set DYLD_LIBRARY_PATH (macOS) / LD_LIBRARY_PATH (Linux)
// or rely on the default homebrew/system prefix.

package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// LocalONNXEmbedder runs sentence-transformer style models entirely
// in-process via ONNX Runtime, with no HTTP roundtrip and no API
// key. The trade-off is build complexity (cgo + two native libs)
// and binary size — operators who don't need this can switch to
// the http backend (ollama / OpenAI compat).
//
// Tested model: BAAI/bge-m3 (XLM-RoBERTa-based, 1024 dims,
// SentencePiece tokenizer). Other sentence-transformers models
// usually work as long as the tokenizer.json is provided alongside.
type LocalONNXEmbedder struct {
	cfg     LocalONNXConfig
	tk      *tokenizers.Tokenizer
	session *ort.DynamicAdvancedSession

	// initOnce guards the global ort.InitializeEnvironment call —
	// the runtime allows only one init per process.
	initOnce sync.Once
	initErr  error
}

// LocalONNXConfig points at the three on-disk artifacts the embedder
// needs. All paths are absolute. Empty values get sensible defaults
// from the operator's homebrew + opendray dep dirs (see the
// build-tag comment for the canonical layout).
type LocalONNXConfig struct {
	// LibraryPath is the directory containing libonnxruntime.dylib
	// (macOS) / libonnxruntime.so (Linux). Required at runtime.
	LibraryPath string
	// ModelPath is the absolute path to the .onnx model file. The
	// model must accept input_ids + attention_mask and return a
	// last_hidden_state tensor (shape [batch, seq, hidden]).
	ModelPath string
	// TokenizerPath is the absolute path to tokenizer.json
	// (the HuggingFace standard format).
	TokenizerPath string
	// MaxSeqLen caps the input length to keep inference predictable.
	// 512 is the bge-m3 default; 0 → 512.
	MaxSeqLen int
	// Hidden dimensions of the model output. 0 = autodetect on the
	// first Embed() call. bge-m3 is 1024, BGESmall is 384.
	Dimensions int
}

// NewLocalONNXEmbedder loads the model + tokenizer and prepares
// an ONNX session for repeated calls. Returns an error if any of
// the artifacts is missing or the runtime can't link against
// onnxruntime.
func NewLocalONNXEmbedder(cfg LocalONNXConfig) (*LocalONNXEmbedder, error) {
	if cfg.LibraryPath == "" {
		return nil, errors.New("memory: LocalONNX requires LibraryPath (dir holding libonnxruntime)")
	}
	if cfg.ModelPath == "" {
		return nil, errors.New("memory: LocalONNX requires ModelPath")
	}
	if cfg.TokenizerPath == "" {
		return nil, errors.New("memory: LocalONNX requires TokenizerPath")
	}
	if cfg.MaxSeqLen <= 0 {
		cfg.MaxSeqLen = 512
	}

	e := &LocalONNXEmbedder{cfg: cfg}
	if err := e.ensureRuntime(); err != nil {
		return nil, err
	}

	tk, err := tokenizers.FromFile(cfg.TokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("memory: load tokenizer %s: %w", cfg.TokenizerPath, err)
	}
	e.tk = tk

	// Models trained for sentence embedding (BERT/XLMR family)
	// always take input_ids + attention_mask. token_type_ids is
	// optional — if the graph requires it we'll see a runtime
	// error and the operator can extend this list.
	inputNames := []string{"input_ids", "attention_mask"}
	outputNames := []string{"last_hidden_state"}
	session, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inputNames, outputNames, nil)
	if err != nil {
		_ = tk.Close()
		return nil, fmt.Errorf("memory: open ONNX session: %w", err)
	}
	e.session = session
	return e, nil
}

func (e *LocalONNXEmbedder) ensureRuntime() error {
	e.initOnce.Do(func() {
		ort.SetSharedLibraryPath(libraryPath(e.cfg.LibraryPath))
		if err := ort.InitializeEnvironment(); err != nil {
			e.initErr = fmt.Errorf("memory: init onnxruntime: %w", err)
		}
	})
	return e.initErr
}

// Close releases the tokenizer + ONNX session resources. Idempotent.
func (e *LocalONNXEmbedder) Close() error {
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
	if e.tk != nil {
		_ = e.tk.Close()
		e.tk = nil
	}
	return nil
}

func (e *LocalONNXEmbedder) Name() string {
	return fmt.Sprintf("local-onnx:%s", baseName(e.cfg.ModelPath))
}

func (e *LocalONNXEmbedder) Dimensions() int { return e.cfg.Dimensions }

// Embed encodes texts in a single batch, runs the ONNX session,
// applies attention-mask-aware mean pooling, and L2-normalises so
// cosine similarity equals the dot product.
func (e *LocalONNXEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// 1. Tokenize each text. tokenizers returns ids + special-token
	//    masks; we don't need offsets so request the cheaper variant.
	inputIDs := make([][]int64, len(texts))
	attnMasks := make([][]int64, len(texts))
	maxLen := 0
	for i, t := range texts {
		ids, _ := e.tk.Encode(t, true)
		if len(ids) > e.cfg.MaxSeqLen {
			ids = ids[:e.cfg.MaxSeqLen]
		}
		row := make([]int64, len(ids))
		mask := make([]int64, len(ids))
		for j, id := range ids {
			row[j] = int64(id)
			mask[j] = 1
		}
		inputIDs[i] = row
		attnMasks[i] = mask
		if len(row) > maxLen {
			maxLen = len(row)
		}
	}
	if maxLen == 0 {
		maxLen = 1 // tokenizer may return empty for empty strings
	}

	// 2. Pad each sequence to maxLen so all rows fit a single
	//    [batch, maxLen] tensor.
	flatIDs := make([]int64, len(texts)*maxLen)
	flatMask := make([]int64, len(texts)*maxLen)
	for i := range texts {
		copy(flatIDs[i*maxLen:], inputIDs[i])
		copy(flatMask[i*maxLen:], attnMasks[i])
	}

	shape := ort.NewShape(int64(len(texts)), int64(maxLen))
	idTensor, err := ort.NewTensor(shape, flatIDs)
	if err != nil {
		return nil, fmt.Errorf("memory: build input_ids tensor: %w", err)
	}
	defer idTensor.Destroy()
	maskTensor, err := ort.NewTensor(shape, flatMask)
	if err != nil {
		return nil, fmt.Errorf("memory: build attention_mask tensor: %w", err)
	}
	defer maskTensor.Destroy()

	// 3. Allocate output. We don't know hidden dim in advance unless
	//    cfg.Dimensions is set; let ORT pick by passing nil and
	//    introspecting after Run.
	inputs := []ort.Value{idTensor, maskTensor}
	outputs := []ort.Value{nil}
	if err := e.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("memory: ONNX run: %w", err)
	}
	hidden, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("memory: unexpected output type %T", outputs[0])
	}
	defer hidden.Destroy()

	// 4. Mean-pool over the sequence dim, weighted by attention mask
	//    (skip padding tokens), then L2-normalise.
	hiddenShape := hidden.GetShape()
	if len(hiddenShape) != 3 {
		return nil, fmt.Errorf("memory: expected [batch, seq, hidden] output, got %v", hiddenShape)
	}
	batch := int(hiddenShape[0])
	seq := int(hiddenShape[1])
	dim := int(hiddenShape[2])
	if e.cfg.Dimensions == 0 {
		e.cfg.Dimensions = dim
	}
	raw := hidden.GetData()

	out := make([][]float32, batch)
	for b := 0; b < batch; b++ {
		vec := make([]float32, dim)
		var weightSum float32
		for s := 0; s < seq; s++ {
			w := float32(attnMasks[b][min(s, len(attnMasks[b])-1)])
			if s >= len(attnMasks[b]) {
				w = 0
			}
			if w == 0 {
				continue
			}
			weightSum += w
			off := (b*seq + s) * dim
			for d := 0; d < dim; d++ {
				vec[d] += raw[off+d] * w
			}
		}
		if weightSum > 0 {
			inv := 1 / weightSum
			for d := 0; d < dim; d++ {
				vec[d] *= inv
			}
		}
		out[b] = l2Normalise(vec)
	}
	return out, nil
}

// libraryPath joins LibraryPath with the platform-specific filename
// for libonnxruntime.
func libraryPath(dir string) string {
	// macOS / Linux dylib naming: libonnxruntime.{dylib,so}.
	// onnxruntime_go's default lookup needs the FILE path, not just
	// the directory.
	for _, name := range []string{"libonnxruntime.dylib", "libonnxruntime.so", "onnxruntime.dll"} {
		full := dir + "/" + name
		if existsFile(full) {
			return full
		}
	}
	// Fall back to letting the loader resolve it via DYLD_LIBRARY_PATH.
	return dir + "/libonnxruntime.dylib"
}

func existsFile(p string) bool {
	if p == "" {
		return false
	}
	// Avoid os.Stat to keep cgo build minimal — we just need
	// "looks like a file path that exists".
	f, err := openReadOnly(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
