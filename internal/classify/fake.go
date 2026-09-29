package classify

import (
	"context"
)

type Fake struct {
	Probabilities []float64
	Model         string
	Err           error
	Calls         []Request
	InputTokens   int
}

func (f *Fake) Judge(ctx context.Context, req Request) (Answers, error) {
	f.Calls = append(f.Calls, req)
	if f.Err != nil {
		return Answers{}, f.Err
	}
	model := f.Model
	if model == "" {
		model = "fake"
	}
	n := len(req.Paragraphs)
	probs := make([]float64, n)
	if len(f.Probabilities) > 0 {
		for i := 0; i < n; i++ {
			if i < len(f.Probabilities) {
				probs[i] = f.Probabilities[i]
			} else {
				probs[i] = f.Probabilities[len(f.Probabilities)-1]
			}
		}
	}
	return Answers{
		Model:         model,
		Probabilities: probs,
		InputTokens:   f.InputTokens,
	}, nil
}
