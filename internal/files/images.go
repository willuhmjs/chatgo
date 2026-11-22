package files

// BE-data owns thumbnails + display-name sanitization:
//   - 512px .thumb jpeg via golang.org/x/image (decode png/jpeg only;
//     others skip the thumb)
//   - Sanitize display names: filepath.Base + length cap
//
// The blank import keeps golang.org/x/image in the module graph until then;
// remove it when implementing.
import _ "golang.org/x/image/draw"
