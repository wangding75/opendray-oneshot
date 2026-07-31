// BrandIcon — render an official-mark SVG for any provider or
// messaging-platform brand we integrate with.
//
// Source mix:
//   - simple-icons npm pkg: official monochrome SVG paths + brand
//     hex colour for everything that ships there (Claude,
//     Telegram, Discord, WeChat).
//   - inline path map: for brands simple-icons does not carry
//     (OpenAI, Slack, Feishu/Lark, DingTalk, WeCom). Paths are
//     drawn to match each brand's well-known mark closely enough
//     that a glance recognises them; they are not pixel-exact
//     copies of the platform's official asset (use the brand
//     guidelines if you need that).
//
// All icons render at the brand's canonical hex colour by default.
// Pass `tone="muted"` to dim by 70% (for compact lists) or
// `color="..."` to override entirely.
//
// Non-component utilities (hasCuratedSvg, brandHex, hasBrandIcon)
// live in brandIconData.ts to satisfy react-refresh/only-export-components.

import {
	CURATED,
	CURATED_MONOCHROME_DARK_INVERT,
	resolve,
} from './brandIconData'

export type { BrandIconKey } from './brandIconData'

export interface BrandIconProps {
	iconKey?: string
	size?: number
	className?: string
	color?: string
	tone?: 'normal' | 'muted'
	title?: string
}

export function BrandIcon({
	iconKey,
	size = 16,
	className,
	color,
	tone = 'normal',
	title,
}: BrandIconProps) {
	const k = iconKey?.toLowerCase()

	// Curated SVG path — use the operator-supplied asset under
	// <base>icons/<key>.svg. import.meta.env.BASE_URL is "/admin/" in
	// the embedded production build and "/" under the dev server, so
	// the asset resolves correctly regardless of mount path. A hardcoded
	// "/admin/" 404s under any other base → SPA fallback HTML → broken
	// <img>. Skips colour/tone overrides because the asset's own colours
	// are the point of using it.
	if (k && CURATED.has(k)) {
		const invert = CURATED_MONOCHROME_DARK_INVERT.has(k)
		const opacity = tone === 'muted' ? 0.72 : 1
		const cls = [
			'inline-block object-contain',
			invert ? 'dark:invert dark:brightness-90' : '',
			className ?? '',
		]
			.filter(Boolean)
			.join(' ')
		return (
			<img
				src={`${import.meta.env.BASE_URL}icons/${k}.svg`}
				width={size}
				height={size}
				alt={title ?? k}
				title={title}
				className={cls}
				style={{ width: size, height: size, opacity }}
			/>
		)
	}

	const data = resolve(iconKey)
	if (!data) return null

	const fill = color ?? `#${data.hex}`
	const opacity = tone === 'muted' ? 0.72 : 1
	const viewBox = data.viewBox ?? '0 0 24 24'

	return (
		<svg
			role="img"
			viewBox={viewBox}
			width={size}
			height={size}
			fill={fill}
			opacity={opacity}
			className={className}
			aria-label={title ?? data.title}
			xmlns="http://www.w3.org/2000/svg"
		>
			<title>{title ?? data.title}</title>
			<path d={data.path} />
		</svg>
	)
}
