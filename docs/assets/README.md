# 品牌素材

- [logo.png](logo.png)：维护者选定的正式 Logo，抽象线条企鹅举杯，红围巾与金色酒杯，透明 PNG。
- [logo-mono.png](logo-mono.png)：同一造型的黑白版本，透明 PNG。
- [qq-group.jpg](qq-group.jpg)：维护者提供的官方体验群二维码原图，群号 **164040130**，未裁剪或重绘。此图是加群二维码，与部署时的机器人授权二维码不同。

Logo 使用内置 image_gen 生成，按维护者反馈完成造型与配色。未使用 CLI 或额外 API Key。项目首页采用 `logo.png`。

## 黑白版提示词

```text
Use case: logo-brand refinement. Edit THIS EXACT ATTACHED SIDE-PROFILE PENGUIN LOGO, chosen by the user, into a slightly cuter version. Keep the existing abstract flowing black line art, side profile facing right, scarf tails flowing left, wing supporting a stemmed wine glass, inner breast line and foot arcs. User request: 这个版本更可爱点，上色不上色版来一下. This call creates the MONOCHROME master. Make head a little larger and rounder, beak shorter and softer, body a little shorter and plumper, tummy pleasantly rounded. Replace the stern pointed leaf eye with a very tiny gentle smiling curved eye stroke. Short rounded feet. The penguin should look gently cheerful and welcoming, not childish or goofy. Preserve elegant grown-up abstract logo styling and open negative space; smooth purposeful charcoal strokes with rounded ends, approximately the same level of structural detail as the source (12-16 lines). Keep the wineglass legible and proportionate, with simple rim ellipse and wine surface line. NO big cartoon eyes, no fill, no shading, no color, no text, no decorative sparkles, no mockup or watermark. Empty transparent interior and genuine transparent background. Single beautifully balanced compact square composition. This is a subtle cuteness refinement of the user's selected side-profile artwork, not a front-facing redesign.
```

## 上色版提示词

```text
Use case: precise-object-edit. Make the COLOR companion of this exact attached cute side-profile penguin line-art logo. Preserve EXACTLY every contour, pose, proportions, smiling eye, short beak, scarf flowing left, inner breast line, raised wing, wine glass, stroke width and framing. Do not redesign anything. Keep it an abstract open LINE LOGO with transparent empty body, no solid black or white silhouette fill. Keep body/eye/wing/glass outline in dark charcoal. Recolor only the two scarf line strokes a tasteful QQ-inspired warm red (#E7473F); add a restrained red accent between the narrow neck scarf lines but keep the flowing tail interior empty. Recolor the short beak stroke and two foot arcs soft amber-gold (#D7A144). Add a small flat amber wine fill (#E7B95E) below the existing wavy liquid line in the cup. No gradients, no rendering, no shadows, no paper texture, no new objects, no lettering, no outline glow, no background, no watermark. Most of the logo stays charcoal and negative space. Transparent background preserved. Match the monochrome source geometry so these feel like a single brand asset pair.
```
