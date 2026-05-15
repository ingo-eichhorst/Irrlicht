# Irrlicht Mascot Image Prompts

Use the three provided Irrlicht flame mascots as visual anchors:

- Purple flame = `working`: focused, energetic, coding, building, streaming output.
- Orange flame = `waiting`: paused, alert, asking for judgment, slightly impatient but helpful.
- Green flame = `ready`: clear, confident, complete, validating, reporting success.

For every prompt, attach the three reference flames and the main style reference image. Ask the image model to preserve the mascots' proportions, face style, flame texture, glow intensity, and color identity. The style reference image is only for prop language and warm lighting, not for background composition.

Style direction: mix modern agent-computing concepts with a dark, warm, Faust-inspired 18th-century steampunk study. The agents can use early steam-powered computers: tiny brass keyboards, dark glass screens, punched-card slips, copper tubes, small boilers, gauges, lenses, aether-network coils, quills beside terminals, vellum printouts, wax seals, and candlelit reflections. Do not build a full room or busy background for these mascot assets. The mascot is the core; the world is only props and ambience around it. Final output must be an isolated transparent PNG cutout with a real alpha channel. No background color, no paper texture field, no gradient, no checkerboard, no shadow plate, no vignette, no rectangular canvas fill.

## Original Mascot Style

Weaker image models need the mascot style spelled out very literally:

- Shape: a soft rounded teardrop flame body with many upward curling flame tongues. The silhouette is asymmetric, organic, and wispy, not a flat icon.
- Edges: soft feathered outer glow and semi-transparent flame tips. Avoid thick outlines, sticker borders, hard vector edges, or plastic 3D contours.
- Rendering: soft watercolor painting mixed with light gouache. The flame should look hand-painted with translucent pigment washes, wet-on-wet color blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain inside the flame, tiny sparkles, and gentle glow. Use layered semi-transparent color washes rather than flat fills or hard gradients. It should not look like flat vector art, anime cel shading, glossy 3D, clay, emoji, sticker art, or a mobile-game icon.
- Light: bright warm inner core near the face and lower body, fading into saturated darker color at the outer flame edges. The flame itself is the light source.
- Face: very simple cute black glossy oval eyes, placed low in the flame body, with one or two crisp white highlights. Small minimal mouth. Eyebrows are short dark curved strokes only when needed for expression.
- Proportions: head/body is one continuous flame, no arms or legs unless tiny flame nubs are needed to hold props. Props must stay secondary and smaller than the flame face area.
- Color recipes: purple uses pale lavender/white core, violet midtones, deep royal-purple outer tongues; green uses yellow-lime core, bright neon green midtones, deeper emerald outer tongues; orange uses yellow-gold core, warm orange midtones, deeper amber outer tongues.
- Props: brass, glass, vellum, and steam-computer props are miniature painterly accessories. They should have warm highlights and hand-painted detail, but must not become photorealistic or dominate the mascot.
- What to avoid: realistic fire, smoke, sparks everywhere, black charred edges, furry texture, hard comic outlines, symmetrical flame spikes, excessive detail, dramatic shadows, rendered backgrounds, any checkerboard pattern, flat digital airbrush, plastic shine, and perfect vector gradients.

## Required OpenAI Image Settings

Do not use `gpt-image-2` for transparent mascot assets. OpenAI currently documents `gpt-image-2` as not supporting transparent backgrounds; `background: "transparent"` is not supported for that model.

Use a GPT Image model that supports transparency, for example `gpt-image-1.5`, `gpt-image-1`, or `gpt-image-1-mini`, and set the output options explicitly:

```json
{
  "model": "gpt-image-1.5",
  "background": "transparent",
  "output_format": "png",
  "quality": "high",
  "size": "1024x1024"
}
```

The transparent background must come from the API setting, not just from the prompt. A checkerboard background means the model rendered fake transparency into opaque pixels; regenerate with `background: "transparent"` on a supported model.

Global constraints to append to every generation:

```text
Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard. Isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Docs Overview

Target file: `mascot-docs-overview-trio.png`

Suggested placement: `site/docs/index.html`

```text
Three Irrlicht flame mascots together as a docs guide team. Purple flame works at a tiny brass computer with a dark glass screen and quill beside it, orange flame holds a small ornate hourglass and question bubble, green flame holds a brass-clipped checklist with abstract checkmarks. Add one small polished brass menu-bar-light charm between them with three colored dots. Keep the trio large and central, with only sparse props and warm candlelit reflections, no full study background.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple Plain

Target file: `mascot-purple-plain.png`

Suggested placement: reusable general-purpose working mascot

```text
Single purple Irrlicht flame mascot, no accessories. Friendly focused expression, simple glossy black oval eyes with crisp white highlights, small calm smile, soft rounded teardrop flame body, many wispy asymmetric flame tongues curling upward. The character should feel like working energy: bright, alert, creative, and in motion, but not frantic. No props, no clothes, no background, no text.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange Plain

Target file: `mascot-orange-plain.png`

Suggested placement: reusable general-purpose waiting mascot

```text
Single orange Irrlicht flame mascot, no accessories. Alert waiting expression, simple glossy black oval eyes with crisp white highlights, small questioning mouth, short curved eyebrows. Soft rounded teardrop flame body with wispy asymmetric flame tongues curling upward. The character should feel like it needs attention or a user decision, but remains cute and helpful. No props, no clothes, no background, no text.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green Plain

Target file: `mascot-green-plain.png`

Suggested placement: reusable general-purpose ready mascot

```text
Single green Irrlicht flame mascot, no accessories. Confident ready expression, simple glossy black oval eyes with crisp white highlights, small upbeat smile. Soft rounded teardrop flame body with wispy asymmetric flame tongues curling upward. The character should feel clear, complete, and available for the next instruction. No props, no clothes, no background, no text.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple With Small Accessories

Target file: `mascot-purple-steam-computer.png`

Suggested placement: reusable working mascot with light Faust/steampunk detail

```text
Single purple Irrlicht flame mascot with only small accessories: a tiny brass keyboard and a dark glass mini screen with abstract glowing marks, plus a quill resting nearby. The mascot remains the dominant subject; accessories are small, adjacent, and secondary. Friendly focused expression, simple glossy black oval eyes with crisp white highlights, small calm smile, soft rounded teardrop flame body with wispy asymmetric flame tongues. No room, no desk, no background, no readable text.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Accessories should be miniature hand-painted brass/glass objects with warm highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange With Small Accessories

Target file: `mascot-orange-hourglass-question.png`

Suggested placement: reusable waiting mascot with light Faust/steampunk detail

```text
Single orange Irrlicht flame mascot with only small accessories: a tiny ornate brass hourglass and a small question bubble charm. The mascot remains the dominant subject; accessories are small, adjacent, and secondary. Alert waiting expression, simple glossy black oval eyes with crisp white highlights, small questioning mouth, short curved eyebrows, soft rounded teardrop flame body with wispy asymmetric flame tongues. No room, no desk, no background, no readable text.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Accessories should be miniature hand-painted brass/glass objects with warm highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green With Small Accessories

Target file: `mascot-green-checklist-status.png`

Suggested placement: reusable ready mascot with light Faust/steampunk detail

```text
Single green Irrlicht flame mascot with only small accessories: a tiny brass-clipped checklist with abstract checkmarks and a small dark-glass status tile with a checkmark. The mascot remains the dominant subject; accessories are small, adjacent, and secondary. Confident ready expression, simple glossy black oval eyes with crisp white highlights, small upbeat smile, soft rounded teardrop flame body with wispy asymmetric flame tongues. No room, no desk, no background, no readable text.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Accessories should be miniature hand-painted brass/glass/vellum objects with warm highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple Reading Logs

Target file: `mascot-purple-reading-logs.png`

Suggested placement: reusable working mascot for observability, replay, transcripts, logs

```text
Single purple Irrlicht flame mascot reading a short curling vellum log strip. The strip has only abstract marks, dots, tiny colored bars, and no readable text. Add one tiny brass magnifying lens near the vellum, smaller than the face. The mascot looks focused and curious, with glossy black oval eyes, crisp white highlights, and a small concentrated smile. No desk, no room, no background, no UI screenshot.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Accessories should be miniature hand-painted vellum, brass, and glass objects with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple Writing Notes

Target file: `mascot-purple-writing-notes.png`

Suggested placement: reusable working mascot for docs, planning, explanation

```text
Single purple Irrlicht flame mascot writing on a small vellum sheet with a tiny quill. The sheet contains only abstract marks and simple diagram strokes, no readable text. Add a tiny brass ink bottle and one small purple code sparkle. The mascot feels thoughtful and productive, slightly leaning toward the page, with a calm smile and bright glossy eyes. No desk, no room, no background.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Accessories should be miniature hand-painted vellum, brass, quill, and glass objects with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple Punched Cards

Target file: `mascot-purple-punched-cards.png`

Suggested placement: reusable working mascot for automation, queues, adapters, background jobs

```text
Single purple Irrlicht flame mascot carrying two or three tiny punched-card slips. The cards are cream vellum with small abstract holes and dots, no readable text. Add a tiny brass clip and a few small violet motion sparks to imply active processing. The mascot looks energetic, competent, and busy, but still cute and simple. No background, no machine, no large props.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Accessories should be miniature hand-painted vellum and brass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Purple Tiny Steam Terminal

Target file: `mascot-purple-tiny-steam-terminal.png`

Suggested placement: reusable working mascot for CLI, coding, command execution

```text
Single purple Irrlicht flame mascot using a very small early steam-powered terminal accessory. The terminal is only a tiny brass keyboard, a palm-sized dark glass screen with abstract glowing strokes, one copper tube, and a little gauge. The mascot remains much larger than the terminal and is the clear subject. Expression: focused, friendly, actively working. No readable command text, no full computer station, no desk, no room.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: pale lavender/white core, violet midtones, deep royal-purple outer tongues. Accessories should be miniature hand-painted brass, glass, and copper with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange Decision Prompt

Target file: `mascot-orange-decision-prompt.png`

Suggested placement: reusable waiting mascot for confirmations, approvals, blocked tasks

```text
Single orange Irrlicht flame mascot holding a tiny brass-framed decision card with two abstract choice marks. The card must have no readable words, only symbolic marks. Add a small question bubble charm and a tiny candlelit reflection on the brass frame. The mascot looks alert and politely impatient, eyebrows slightly raised, eyes glossy and clear, small questioning mouth. No background, no UI screenshot, no large sign.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Accessories should be miniature hand-painted brass and vellum with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange Listening

Target file: `mascot-orange-listening.png`

Suggested placement: reusable waiting mascot for user input, pending response, watch mode

```text
Single orange Irrlicht flame mascot listening carefully beside a tiny brass ear-trumpet shaped like an aether receiver. Add two or three small amber signal waves and a tiny dark-glass indicator dot, but no readable text. The mascot looks attentive, patient, and ready for input, with soft raised eyebrows and a small neutral mouth. Keep props very small and secondary.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Accessories should be miniature hand-painted brass, copper, and dark glass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange Timeout Hourglass

Target file: `mascot-orange-timeout-hourglass.png`

Suggested placement: reusable waiting mascot for delays, background work, queued action

```text
Single orange Irrlicht flame mascot sitting beside a tiny ornate brass hourglass. Add a very small winding key and two soft amber circular wait arrows around the hourglass, not around the whole mascot. The mascot looks mildly impatient but still friendly: one eyebrow raised, small pursed mouth, glossy black eyes. The hourglass must be smaller than the mascot face area. No background, no progress bar, no text.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Accessories should be miniature hand-painted brass and glass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Orange Caution Lantern

Target file: `mascot-orange-caution-lantern.png`

Suggested placement: reusable waiting mascot for warnings, unsafe settings, review required

```text
Single orange Irrlicht flame mascot holding a tiny brass safety lantern with warm amber glass. Add one very small caution triangle charm beside the lantern, with no exclamation mark and no readable text. The mascot looks serious but not angry: focused eyes, slight eyebrow angle, small concerned mouth. The lantern should glow softly but not overpower the orange flame. No background, no room, no large warning sign.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-gold core, warm orange midtones, deeper amber outer tongues. Accessories should be miniature hand-painted brass and glass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green Success Seal

Target file: `mascot-green-success-seal.png`

Suggested placement: reusable ready mascot for success, completion, release-ready states

```text
Single green Irrlicht flame mascot presenting a tiny green wax seal stamped with a simple abstract checkmark. Add a small brass ribbon clip and a tiny vellum corner, but no readable text. The mascot looks proud, calm, and complete, with glossy black oval eyes and a small confident smile. The seal and vellum must be much smaller than the mascot and held close to the body. No background, no document page.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Accessories should be miniature hand-painted wax, vellum, and brass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green Monitoring Gauge

Target file: `mascot-green-monitoring-gauge.png`

Suggested placement: reusable ready mascot for health checks, dashboards, status monitoring

```text
Single green Irrlicht flame mascot beside a tiny brass monitoring gauge and a dark-glass status tile. The gauge needle points to a safe zone shown only by abstract green marks, no text or numbers. Add a few small green signal dots. The mascot looks observant and satisfied, with glossy black eyes and a soft smile. Keep the gauge and tile small and adjacent, not a full dashboard.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Accessories should be miniature hand-painted brass and dark glass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green Guide Lantern

Target file: `mascot-green-guide-lantern.png`

Suggested placement: reusable ready mascot for onboarding, next steps, docs navigation

```text
Single green Irrlicht flame mascot carrying a tiny brass guide lantern with pale green glass. The lantern casts only a very small soft glow around itself, not a background. Add one small compass charm or etched brass pointer beside the lantern. The mascot looks welcoming and confident, like it is ready to guide the user onward. No background, no path, no room, no readable text.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Accessories should be miniature hand-painted brass and green glass with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## General Solo: Green Archive Ledger

Target file: `mascot-green-archive-ledger.png`

Suggested placement: reusable ready mascot for history, stored sessions, replay, audit trail

```text
Single green Irrlicht flame mascot holding a tiny closed archive ledger with a brass corner and small green bookmark ribbon. Add a miniature wax seal with an abstract checkmark. The ledger has no readable title or letters. The mascot looks calm, reliable, and finished, with glossy black eyes and a small satisfied smile. Keep all accessories small and close to the body. No background, no shelf, no stack of books.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Color recipe: yellow-lime core, bright neon green midtones, deeper emerald outer tongues. Accessories should be miniature hand-painted leather, vellum, brass, and wax with warm Faust-era steampunk highlights, not photorealistic. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Observing Agents: Purple Watcher

Target file: `mascot-purple-observing-agent-terminal.png`

Suggested placement: reusable mascot for watching an active agent session

```text
Single purple Irrlicht flame mascot observing a tiny abstract agent at work inside a palm-sized dark-glass terminal. The agent is not a person and not a robot: represent it only as a small glowing generic glyph, moving dots, and a few abstract event sparks behind the glass. Purple is watching carefully through a tiny brass monocle lens, calm and focused, not controlling the agent. The props are very small; the mascot remains the main subject. No readable text, no brand logos, no large UI, no background room.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for brass/glass/steam-computing props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Observing Agents: Orange Waiting On Agent

Target file: `mascot-orange-observing-agent-waiting.png`

Suggested placement: reusable mascot for an agent that is running but needs a decision soon

```text
Single orange Irrlicht flame mascot watching a tiny abstract agent glyph inside a small brass-and-glass status capsule. The capsule shows only three small glowing activity dots and one tiny question bubble charm, no readable text. Orange looks attentive and slightly impatient, as if waiting for the agent to reach a decision point. The mascot observes rather than operates the system. Keep the capsule small and adjacent; no full computer, no dashboard, no background.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for brass/glass/steam-computing props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Observing Agents: Green Verified Agent

Target file: `mascot-green-observing-agent-verified.png`

Suggested placement: reusable mascot for complete sessions, healthy agents, successful monitoring

```text
Single green Irrlicht flame mascot observing a tiny abstract agent glyph on a small dark-glass status tile. The tile has a brass rim, a few green event dots, and a small checkmark charm, but no readable text or real UI. Green looks confident and satisfied, as if the observed agent finished cleanly. The mascot is clearly watching and validating, not controlling. Keep all props small and secondary; no background.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for brass/glass/steam-computing props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Observing Agents: Trio Session Watch

Target file: `mascot-trio-observing-agent-session.png`

Suggested placement: reusable hero or docs image for Irrlicht observing agents

```text
Three Irrlicht flame mascots observing a tiny abstract agent session together. Purple studies a palm-sized dark-glass terminal with moving event dots, orange watches a small question bubble and hourglass beside it, green checks a tiny brass-rimmed status tile with an abstract checkmark. The agent itself is only a generic glowing glyph and event trail inside the tiny terminal, not a human, not a robot, not a branded logo. The trio should read as observe, understand, illuminate: they monitor without controlling. Mascots dominate the image; props are small, sparse, and Faust-era steampunk.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Observing Agents: Purple And Green Review

Target file: `mascot-duo-purple-green-agent-review.png`

Suggested placement: reusable docs image for reviewing agent output and validating results

```text
Purple and green Irrlicht flame mascots observing an abstract agent output stream. Purple reads a small curling vellum transcript strip with abstract marks, while green compares it to a tiny brass checklist with checkmarks. Between them is a palm-sized dark-glass agent tile showing only a generic glowing glyph and two event dots. The mood is careful review and validation, not control. Keep the duo large and central, props tiny and secondary, no readable text, no background.

Use the provided purple and green Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Installation

Target file: `mascot-installation-duo.png`

Suggested placement: `site/docs/installation.html`

```text
Purple and green Irrlicht flame mascots installing the app with restrained steampunk-computing props. Purple carries a small sealed brass-and-paper software cartridge with a wax seal, while green gives a confident checkmark gesture beside a tiny glowing menu-bar icon set into a brass dial. Add a simple etched-brass download arrow charm and a tiny punched-card slip. Keep the two mascots large and central; props should be small accessories only.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Quick Start

Target file: `mascot-quickstart-trio.png`

Suggested placement: `site/docs/quickstart.html`

```text
Three Irrlicht flame mascots walking through the first run. Purple flame touches a tiny steam-powered command terminal with brass keys and abstract glowing marks on dark glass, green flame points at a small glowing menu-bar-light charm in a polished brass frame, orange flame peeks in with a question bubble for the next user action. Add only a quill, a tiny gauge, and warm candlelit reflections. Keep the scene simple and mascot-led.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## The Light System

Target file: `mascot-light-system-trio.png`

Suggested placement: `site/docs/light-system.html`

```text
Three Irrlicht flame mascots standing side by side as the state model. Purple actively uses a tiny brass keyboard connected to a dark glass screen with tiny abstract marks, orange pauses with a raised hand, question bubble, and ornate hourglass, green rests with a brass checkmark badge and small status ledger. The image should read as working, waiting, ready at a glance. Accessories should be tiny and understated; no full setting.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## State Machine

Target file: `mascot-state-machine-trio.png`

Suggested placement: `site/docs/state-machine.html`

```text
Purple, orange, and green Irrlicht flame mascots arranged along a simple circular transition path made of soft glowing arrows with a faint alchemical-diagram feel. Purple moves with tiny code sparks from a brass terminal, orange stops beside an hourglass and question bubble, green finishes the loop with a calm checkmark on a small dark-glass status tile. Character-first and simple, showing deterministic state transitions without text. Keep arrows and props light and sparse.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Session Detection

Target file: `mascot-session-detection-duo.png`

Suggested placement: `site/docs/session-detection.html`

```text
Purple and green Irrlicht flame mascots investigating session discovery. Purple follows a short glowing vellum transcript strip feeding out of a tiny steam terminal, while green uses a brass-rimmed magnifying glass over a few process dots, file-change sparks, and punched-card slips. Technical detective mood, but simple and mascot-centered. Use no real filenames, no readable text, and no full desk or room.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Configuration

Target file: `mascot-configuration-duo.png`

Suggested placement: `site/docs/configuration.html`

```text
Green and orange Irrlicht flame mascots with a compact brass settings charm. Green calmly adjusts two tiny toggle levers, a small gauge, and a dark-glass network dial, while orange holds a small warning triangle near a simple glowing aether-signal icon to imply careful LAN exposure. Practical sysadmin mood, restrained and clear. No full control panel, no readable labels.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## CLI Tools

Target file: `mascot-cli-tools-duo.png`

Suggested placement: `site/docs/cli-tools.html`

```text
Purple and green Irrlicht flame mascots working with a tiny steam-powered computer. Purple turns a small key beside brass keys and a dark glass slate with abstract glowing lines, while green watches three colored status dots on a small mechanical ticker. Terminal utility mood translated into restrained Faust-era computing props. Crisp and minimal, no readable command text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## API Reference

Target file: `mascot-api-reference-duo.png`

Suggested placement: `site/docs/api-reference.html`

```text
Green and purple Irrlicht flame mascots demonstrating real-time API updates through a tiny aether-network computer accessory. Green sends a few glowing data sparks through a small brass coil, copper tube, and glass connector, while purple receives them on a dark glass tile with colored dots. Show HTTP and WebSocket flow using simple light trails and brass fittings only. No readable endpoint names, no large apparatus.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## System Design

Target file: `mascot-architecture-trio.png`

Suggested placement: `site/docs/architecture.html`

```text
Three Irrlicht flame mascots building a small layered architecture model. Purple places one glowing core block beside a tiny brass computer module, green connects a tiny bridge made from copper tubes and dark glass, orange guards a boundary with a small caution sign and measuring compass. Clean ports-and-adapters metaphor, structured but cute. The model should be small compared to the mascots; no large orrery, no complex machine, no readable text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Adapters

Target file: `mascot-adapters-trio.png`

Suggested placement: `site/docs/adapters.html`

```text
Three Irrlicht flame mascots wiring agent adapters using small steampunk-computing accessories. Purple plugs two braided copper cables into a central glowing Irrlicht light charm, green verifies the connections with a brass-clipped checklist and tiny dark-glass status tile, orange watches one loose cable with a thoughtful question bubble. Use only generic plug shapes and tiny abstract agent nodes. No real brand logos, no names, no large laboratory network.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Contributing

Target file: `mascot-contributing-trio.png`

Suggested placement: `site/docs/contributing.html`

```text
Three Irrlicht flame mascots collaborating on an open-source pull request with subtle scholarly computing props. Purple edits glowing abstract marks on a tiny brass computer with dark glass screen, orange asks a thoughtful review question with a bubble and tiny spectacles, green approves with a checkmark wax seal and small branch/merge symbol made from brass wire. Warm technical collaboration mood, balanced and respectful, no readable text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Code of Conduct

Target file: `mascot-code-of-conduct-duo.png`

Suggested placement: `site/docs/code-of-conduct.html`

```text
Green and orange Irrlicht flame mascots standing calmly beside a small engraved brass shield and checkmark wax seal. The mood is respectful, quiet, and protective, conveying community care and clear boundaries. Keep it restrained and sincere, not comedic. Only small brass and wax accessories, no full setting, no readable text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Changelog

Target file: `mascot-changelog-trio.png`

Suggested placement: `site/docs/changelog.html`

```text
Three Irrlicht flame mascots maintaining a small release timeline. Green pins a fresh vellum printout from a tiny steam computer to a brass timeline, purple adds code sparks from a dark glass terminal, orange checks an ornate pocket watch and progress marker. Version history and release rhythm, lively but organized. Keep the props compact and secondary, no readable version numbers or text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Purple Studying

Target file: `mascot-purple-scholar-studying.png`

Suggested placement: tutorial intros, learning notes, deep-dive explanations

```text
Single purple Irrlicht flame mascot as a curious scholar studying a small open vellum notebook. Add tiny round brass spectacles resting low on the flame, a small quill, and two tiny abstract diagram marks on the page. The notebook must have no readable text. The mascot looks focused, bright, and eager to understand, with glossy black oval eyes and a small thoughtful smile. Keep the scholar accessories minimal and secondary; no desk, no library, no room, no background.

Use the provided purple Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for old scholarly props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Accessories should be miniature hand-painted brass, vellum, and quill objects with warm Faust-era steampunk highlights, not photorealistic. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Orange Asking Why

Target file: `mascot-orange-scholar-question.png`

Suggested placement: conceptual questions, caveats, exercises, decision points

```text
Single orange Irrlicht flame mascot as a questioning scholar. Add a tiny scholar collar, a small brass-framed question bubble charm, and a little closed notebook with an empty label. The mascot looks thoughtful and slightly skeptical, as if asking "why does this work?", with raised eyebrows, glossy black oval eyes, and a small questioning mouth. No readable text, no large question mark sign, no full classroom, no background.

Use the provided orange Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for old scholarly props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Accessories should be miniature hand-painted brass, vellum, and cloth details with warm Faust-era steampunk highlights, not photorealistic. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Green Understanding

Target file: `mascot-green-scholar-understanding.png`

Suggested placement: summaries, solved exercises, concept checkpoints

```text
Single green Irrlicht flame mascot as a confident scholar who has understood the concept. Add a tiny brass-rimmed slate with an abstract checkmark and simple diagram lines, plus a small wax seal and a short vellum bookmark. No readable text or equations. The mascot looks calm, clear, and pleased, with glossy black oval eyes and a small satisfied smile. Keep props tiny and close to the mascot; no blackboard, no classroom, no background.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for old scholarly props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Accessories should be miniature hand-painted brass, slate, vellum, and wax objects with warm Faust-era steampunk highlights, not photorealistic. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Trio Study Circle

Target file: `mascot-trio-scholar-study-circle.png`

Suggested placement: learning-project overview, docs landing, educational framing

```text
Three Irrlicht flame mascots gathered around a very small shared study diagram. Purple adds abstract marks to a tiny vellum page with a quill, orange points at a small question bubble and empty exercise card, green holds a tiny brass-rimmed slate with a checkmark. The trio should feel like a study circle: curiosity, questioning, and understanding. Add only small scholar accessories such as tiny spectacles, quill, vellum, brass compass, and dark glass tile. No full classroom, no bookshelves, no table, no readable text.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only for Faust-era scholarly props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flames like small watercolor paintings with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Purple And Orange Socratic Debug

Target file: `mascot-duo-purple-orange-socratic-debug.png`

Suggested placement: debugging lessons, "think before acting" callouts, exercises

```text
Purple and orange Irrlicht flame mascots in a tiny Socratic debugging moment. Purple examines an abstract event strip from a small dark-glass steam terminal, while orange asks a careful question with a tiny brass question bubble charm and raised eyebrow. Add a small vellum hypothesis card with only abstract marks, no readable text. The mood is learning through questioning, not panic. Mascots dominate; props are compact and secondary. No full desk, no room, no UI screenshot.

Use the provided purple and orange Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only for Faust-era scholarly computing props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flames like small watercolor paintings with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Scholar / Learning: Green Teaching Recap

Target file: `mascot-green-scholar-recap.png`

Suggested placement: lesson summaries, "what you learned" boxes, completion sections

```text
Single green Irrlicht flame mascot teaching a tiny recap with a small brass pointer and a palm-sized dark slate. The slate shows only three abstract dots and a checkmark, no readable text. Add a tiny scholar collar or small spectacles, but keep clothing minimal. The mascot looks patient, encouraging, and precise, like it is explaining the final takeaway. No classroom, no chalkboard wall, no background.

Use the provided green Irrlicht flame mascot reference image as the strict character anchor and the supplied ambience image as style reference only for old scholarly props. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Accessories should be miniature hand-painted brass, slate, and vellum objects with warm Faust-era steampunk highlights, not photorealistic. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha. Square 1254x1254 composition, clean edges, no watermark.
```

## Reusable Spot: Working

Target file: `mascot-purple-deep-work.png`

Suggested placement: callouts explaining `working`

```text
Purple Irrlicht flame mascot in deep focused work mode, with tiny brass goggles, a tiny brass keyboard, and a dark glass screen showing abstract glowing marks. Add a quill, a few tiny file icons, and tool sparks. The expression is happy and concentrated, showing active processing without needing user attention. The mascot should dominate; props are minimal.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Reusable Spot: Waiting

Target file: `mascot-orange-needs-input.png`

Suggested placement: callouts explaining `waiting`

```text
Orange Irrlicht flame mascot paused and asking for user input, with a tiny scholar collar, a small question bubble, an ornate hourglass, and a tiny brass terminal waiting for input on a blank dark glass screen. Add a small empty parchment response slip with no text. The expression is alert and slightly impatient but friendly, clearly showing the session needs judgment before continuing. Keep props sparse and close to the mascot.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```

## Reusable Spot: Ready

Target file: `mascot-green-complete.png`

Suggested placement: callouts explaining `ready`

```text
Green Irrlicht flame mascot finished and ready for the next instruction, with a tiny neat waistcoat collar, a small brass-clipped checklist with abstract checkmarks, a dark-glass status tile with a checkmark, and one polished pocket-watch or lens accent. The expression is confident, fresh, and available. Keep the mascot dominant and the accessories understated.

Use the provided three Irrlicht flame mascot reference images as strict character anchors and the supplied ambience image as style reference only. Preserve the original mascot style exactly: soft rounded teardrop flame body, wispy asymmetric flame tongues, feathered semi-transparent edges, bright inner glow, and simple glossy black oval eyes with crisp white highlights. Render the flame like a small watercolor painting with light gouache accents: translucent pigment washes, wet-on-wet blending, visible soft brush texture, uneven watercolor blooms, subtle paper-like grain, layered semi-transparent color bands, tiny sparkles, and gentle glow. Avoid flat vector art, perfect digital gradients, sticker borders, thick outlines, realistic fire, emoji style, glossy 3D, anime cel shading, hard shadows, plastic rendering, and flat digital airbrush. The mascot must occupy most of the image and remain the clear subject. Add only restrained Faust-inspired steampunk-computing accessories: tiny brass keyboard, dark glass screen, punched-card slip, copper tube, small boiler, gauge, lens, aether-network coil, quill, vellum printout, wax seal, chalk mark, or candlelit reflection. Do not create a full room, scenic background, large furniture, heavy costume, complex machinery, readable text, brand logos, or UI screenshots. Output a PNG with a real transparent alpha channel using the API background setting, not a rendered checkerboard: isolated mascot cutout only. Background must be fully transparent alpha, not white, not black, not beige, not parchment, not dark, not gradient, not checkerboard, and not a rendered room. Keep only the mascot and its small handheld/adjacent props. Square 1254x1254 composition, clean edges, no watermark.
```
