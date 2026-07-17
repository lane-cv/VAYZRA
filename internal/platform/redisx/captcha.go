package redisx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const captchaTTL = 5 * time.Minute

var ErrCaptchaRateLimited = errors.New("captcha issuance rate limited")

type CaptchaPolicy struct {
	Window time.Duration
	PerIP  int
	Global int
}

func (p CaptchaPolicy) normalized() CaptchaPolicy {
	if p.Window <= 0 {
		p.Window = captchaTTL
	}
	if p.PerIP <= 0 {
		p.PerIP = 10
	}
	if p.Global <= 0 {
		p.Global = 1000
	}
	return p
}

// Challenge is returned to the browser as an image body and opaque ID header.
type Challenge struct {
	ID  string
	PNG []byte
}

// CaptchaService is consumed by the authentication HTTP boundary.
type CaptchaService interface {
	Create(context.Context, string) (Challenge, error)
	Verify(context.Context, string, string) (bool, error)
}

// CaptchaStore holds only HMACed answers in Redis. The plaintext answer exists
// only while Create renders the server-generated image.
type CaptchaStore struct {
	rdb    *redis.Client
	secret []byte
	random io.Reader
	policy CaptchaPolicy
	render func(string, io.Reader) ([]byte, error)
}

func NewCaptchaStore(rdb *redis.Client, secret []byte) *CaptchaStore {
	return NewCaptchaStoreWithPolicy(rdb, secret, CaptchaPolicy{})
}

func NewCaptchaStoreWithPolicy(rdb *redis.Client, secret []byte, policy CaptchaPolicy) *CaptchaStore {
	copySecret := append([]byte(nil), secret...)
	if len(copySecret) == 0 {
		copySecret = make([]byte, 32)
		if _, err := rand.Read(copySecret); err != nil {
			panic("read captcha secret")
		}
	}
	return &CaptchaStore{rdb: rdb, secret: copySecret, random: rand.Reader, policy: policy.normalized(), render: renderCaptcha}
}

func (s *CaptchaStore) Create(ctx context.Context, ip string) (Challenge, error) {
	if s.rdb == nil || ip == "" {
		return Challenge{}, fmt.Errorf("captcha storage unavailable")
	}
	idBytes := make([]byte, 32)
	if _, err := io.ReadFull(s.random, idBytes); err != nil {
		return Challenge{}, fmt.Errorf("generate captcha ID: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	answer, err := s.newAnswer()
	if err != nil {
		return Challenge{}, err
	}
	policy := s.policy.normalized()
	stored, err := issueCaptchaScript.Run(ctx, s.rdb, []string{s.ipKey(ip), s.globalKey(), s.key(id)}, policy.PerIP, policy.Global, policy.Window.Milliseconds(), s.answerHash(answer), captchaTTL.Milliseconds()).Int()
	if err != nil {
		return Challenge{}, fmt.Errorf("store captcha: %w", err)
	}
	if stored != 1 {
		return Challenge{}, ErrCaptchaRateLimited
	}
	pngBody, err := s.render(answer, s.random)
	if err != nil {
		return Challenge{}, err
	}
	if len(pngBody) == 0 || len(pngBody) > 50*1024 {
		return Challenge{}, fmt.Errorf("render captcha image")
	}
	return Challenge{ID: id, PNG: pngBody}, nil
}

var issueCaptchaScript = redis.NewScript(`
local ip = tonumber(redis.call('GET', KEYS[1]) or '0')
local global = tonumber(redis.call('GET', KEYS[2]) or '0')
if ip >= tonumber(ARGV[1]) or global >= tonumber(ARGV[2]) then return 0 end
ip = redis.call('INCR', KEYS[1])
if ip == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[3]) end
global = redis.call('INCR', KEYS[2])
if global == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[3]) end
redis.call('PSETEX', KEYS[3], ARGV[5], ARGV[4])
return 1
`)

func (s *CaptchaStore) Verify(ctx context.Context, id, answer string) (bool, error) {
	if s.rdb == nil || id == "" {
		return false, fmt.Errorf("captcha storage unavailable")
	}
	stored, err := consumeCaptchaScript.Run(ctx, s.rdb, []string{s.key(id)}).Text()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consume captcha: %w", err)
	}
	return hmac.Equal([]byte(stored), []byte(s.answerHash(answer))), nil
}

var consumeCaptchaScript = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if value then redis.call('DEL', KEYS[1]) end
return value
`)

func (s *CaptchaStore) newAnswer() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPRSTUVWXYZ"
	var answer strings.Builder
	answer.Grow(5)
	limit := big.NewInt(int64(len(alphabet)))
	for range 5 {
		index, err := rand.Int(s.random, limit)
		if err != nil {
			return "", fmt.Errorf("generate captcha answer: %w", err)
		}
		answer.WriteByte(alphabet[index.Int64()])
	}
	return answer.String(), nil
}

func (s *CaptchaStore) key(id string) string { return s.hmacKey("captcha-id", id) }
func (s *CaptchaStore) ipKey(ip string) string {
	return "hl:captcha:ip:" + s.hmacKey("captcha-ip", ip)[11:]
}
func (s *CaptchaStore) globalKey() string { return "hl:captcha:global" }
func (s *CaptchaStore) hmacKey(namespace, value string) string {
	h := hmac.New(sha256.New, s.secret)
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	return "hl:captcha:" + hex.EncodeToString(h.Sum(nil))
}

func (s *CaptchaStore) answerHash(answer string) string {
	h := hmac.New(sha256.New, s.secret)
	_, _ = h.Write([]byte("captcha-answer"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.ToUpper(strings.TrimSpace(answer))))
	return hex.EncodeToString(h.Sum(nil))
}

type glyphTransform struct {
	x, y           int
	scaleX, scaleY int
	shear          int
	rotation       int
}

func renderCaptcha(answer string, random io.Reader) ([]byte, error) {
	imageBody, _, err := renderCaptchaImage(answer, random)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, imageBody); err != nil {
		return nil, fmt.Errorf("encode captcha: %w", err)
	}
	return out.Bytes(), nil
}

func renderCaptchaImage(answer string, random io.Reader) (*image.RGBA, []glyphTransform, error) {
	imageBody := image.NewRGBA(image.Rect(0, 0, 140, 48))
	draw.Draw(imageBody, imageBody.Bounds(), &image.Uniform{C: color.RGBA{245, 248, 252, 255}}, image.Point{}, draw.Src)
	noise := make([]byte, 192)
	if _, err := io.ReadFull(random, noise); err != nil {
		return nil, nil, fmt.Errorf("generate captcha distortion: %w", err)
	}
	for offset := 0; offset+2 < len(noise); offset += 3 {
		x, y := int(noise[offset])%140, int(noise[offset+1])%48
		imageBody.Set(x, y, color.RGBA{80 + noise[offset+2]%120, 100, 150, 150})
	}
	for x := 0; x < 140; x += 7 {
		y := 5 + int(noise[x%len(noise)]%35)
		imageBody.Set(x, y, color.RGBA{150, 170, 195, 180})
	}
	transformBytes := make([]byte, len(answer)*6)
	if _, err := io.ReadFull(random, transformBytes); err != nil {
		return nil, nil, fmt.Errorf("generate captcha glyph transforms: %w", err)
	}
	transforms := make([]glyphTransform, 0, len(answer))
	for index, letter := range answer {
		offset := index * 6
		transform := glyphTransform{
			x:        8 + index*26 + int(transformBytes[offset]%5) - 2,
			y:        8 + int(transformBytes[offset+1]%5) - 2,
			scaleX:   3 + int(transformBytes[offset+2]%2),
			scaleY:   3 + int(transformBytes[offset+3]%2),
			shear:    int(transformBytes[offset+4]%3) - 1,
			rotation: int(transformBytes[offset+5]%7) - 3,
		}
		drawGlyph(imageBody, letter, color.RGBA{25 + uint8(index*23), 55, 105 + uint8(index*19), 255}, transform)
		transforms = append(transforms, transform)
	}
	return imageBody, transforms, nil
}

var glyphs = map[rune][]string{
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"}, 'B': {"11110", "10001", "11110", "10001", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"}, 'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "11110", "10000", "10000", "10000", "11111"}, 'F': {"11111", "10000", "11110", "10000", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10111", "10001", "10001", "01111"}, 'H': {"10001", "10001", "11111", "10001", "10001", "10001", "10001"},
	'J': {"00111", "00010", "00010", "00010", "10010", "10010", "01100"}, 'K': {"10001", "10010", "11100", "10100", "10010", "10001", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"}, 'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"}, 'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"}, 'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"}, 'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"}, 'W': {"10001", "10001", "10001", "10101", "10101", "10101", "01010"},
	'X': {"10001", "01010", "00100", "00100", "00100", "01010", "10001"}, 'Y': {"10001", "01010", "00100", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}

func drawGlyph(dst *image.RGBA, letter rune, c color.Color, transform glyphTransform) {
	pattern := glyphs[letter]
	centerX := float64(5*transform.scaleX) / 2
	centerY := float64(7*transform.scaleY) / 2
	angle := float64(transform.rotation) * math.Pi / 180
	cosine, sine := math.Cos(angle), math.Sin(angle)
	for row, line := range pattern {
		for column, value := range line {
			if value != '1' {
				continue
			}
			for dy := 0; dy < transform.scaleY; dy++ {
				for dx := 0; dx < transform.scaleX; dx++ {
					glyphX := float64(column*transform.scaleX + dx)
					glyphY := float64(row*transform.scaleY + dy)
					shearedX := glyphX + float64((row-3)*transform.shear)
					x := int(math.Round(float64(transform.x) + centerX + (shearedX-centerX)*cosine - (glyphY-centerY)*sine))
					y := int(math.Round(float64(transform.y) + centerY + (shearedX-centerX)*sine + (glyphY-centerY)*cosine))
					if image.Pt(x, y).In(dst.Bounds()) {
						dst.Set(x, y, c)
					}
				}
			}
		}
	}
}
