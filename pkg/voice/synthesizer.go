package voice

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Synthesizer is the TTS counterpart to the Transcriber interface.
// Implementations convert text to audible speech output.
type Synthesizer interface {
	Name() string
	Speak(ctx context.Context, text string) error
}

// TermuxSynthesizer uses the termux-tts-speak command for offline TTS on Android.
// Requires the termux-api package: pkg install termux-api
type TermuxSynthesizer struct {
	// Language BCP-47 tag (e.g. "en-US", "en-AU"). Empty = system default.
	Language string
	// SpeechRate multiplier. 1.0 = normal, 0.5 = slow, 2.0 = fast.
	Rate float64
	// Pitch multiplier. 1.0 = normal.
	Pitch float64
}

// NewTermuxSynthesizer creates a TTS synthesizer that uses termux-tts-speak.
// It validates that the command is available in PATH.
func NewTermuxSynthesizer(language string, rate, pitch float64) (*TermuxSynthesizer, error) {
	if _, err := exec.LookPath("termux-tts-speak"); err != nil {
		return nil, fmt.Errorf("termux-tts-speak not found: install termux-api package (pkg install termux-api): %w", err)
	}

	if rate <= 0 {
		rate = 1.0
	}
	if pitch <= 0 {
		pitch = 1.0
	}

	return &TermuxSynthesizer{
		Language: language,
		Rate:     rate,
		Pitch:    pitch,
	}, nil
}

func (s *TermuxSynthesizer) Name() string {
	return "termux"
}

// Speak converts text to speech using termux-tts-speak.
// It streams text via stdin to avoid argument length limits.
func (s *TermuxSynthesizer) Speak(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}

	// Truncate very long responses to avoid blocking the agent for minutes.
	const maxChars = 2000
	if len(text) > maxChars {
		text = text[:maxChars] + "... (truncated)"
	}

	args := []string{}
	if s.Language != "" {
		args = append(args, "-l", s.Language)
	}
	if s.Rate != 1.0 {
		args = append(args, "-r", fmt.Sprintf("%.1f", s.Rate))
	}
	if s.Pitch != 1.0 {
		args = append(args, "-p", fmt.Sprintf("%.1f", s.Pitch))
	}

	cmd := exec.CommandContext(ctx, "termux-tts-speak", args...)
	cmd.Stdin = strings.NewReader(text)

	logger.InfoCF("voice", "TTS speaking", map[string]any{
		"engine":      "termux",
		"text_length": len(text),
		"language":    s.Language,
	})

	if err := cmd.Run(); err != nil {
		logger.ErrorCF("voice", "TTS failed", map[string]any{"error": err})
		return fmt.Errorf("termux-tts-speak failed: %w", err)
	}

	return nil
}

// ExecSynthesizer uses any external command for TTS.
// Useful for desktop Linux (espeak, festival, piper) or macOS (say).
type ExecSynthesizer struct {
	Command string   // e.g. "espeak", "say", "piper"
	Args    []string // extra args before text
}

func NewExecSynthesizer(command string, args ...string) (*ExecSynthesizer, error) {
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("TTS command %q not found in PATH: %w", command, err)
	}
	return &ExecSynthesizer{Command: command, Args: args}, nil
}

func (s *ExecSynthesizer) Name() string {
	return "exec:" + s.Command
}

func (s *ExecSynthesizer) Speak(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}

	const maxChars = 2000
	if len(text) > maxChars {
		text = text[:maxChars] + "... (truncated)"
	}

	args := make([]string, len(s.Args))
	copy(args, s.Args)
	args = append(args, text)

	cmd := exec.CommandContext(ctx, s.Command, args...)

	logger.InfoCF("voice", "TTS speaking", map[string]any{
		"engine":      s.Command,
		"text_length": len(text),
	})

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s TTS failed: %w", s.Command, err)
	}
	return nil
}

// DetectSynthesizer inspects the environment and returns the best available
// TTS synthesizer, or nil if none is available.
func DetectSynthesizer(language string, rate, pitch float64) Synthesizer {
	// Prefer termux on Android
	if synth, err := NewTermuxSynthesizer(language, rate, pitch); err == nil {
		logger.InfoCF("voice", "Detected TTS engine", map[string]any{"engine": "termux"})
		return synth
	}

	// Fallback: try common Linux TTS commands
	for _, cmd := range []string{"espeak", "espeak-ng", "piper"} {
		if synth, err := NewExecSynthesizer(cmd); err == nil {
			logger.InfoCF("voice", "Detected TTS engine", map[string]any{"engine": cmd})
			return synth
		}
	}

	logger.WarnCF("voice", "No TTS engine found. Install termux-api (Android) or espeak (Linux).", nil)
	return nil
}
