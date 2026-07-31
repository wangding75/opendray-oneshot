// BrandAvatar — circular badge with an inscribed BrandIcon and a
// brand-coloured background tint. Falls back to a letter avatar
// (single uppercase character on a neutral disc) when the iconKey
// is unknown.
//
// Used everywhere the admin currently shows a coloured letter:
// Sessions list, Channels list, Provider headers.

import { cn } from '@/lib/utils'
import { BrandIcon } from './BrandIcon'
import { brandHex, hasBrandIcon } from './brandIconData'

export interface BrandAvatarProps {
	iconKey?: string
	// fallbackLetter — single character shown when iconKey is
	// unknown. Defaults to '?' so missing data is visually
	// noticeable.
	fallbackLetter?: string
	// fallbackColor — tint hex (no leading #) for the fallback
	// background. Defaults to a neutral grey.
	fallbackColor?: string
	size?: number
	className?: string
	// title — accessibility label and hover tooltip.
	title?: string
}

export function BrandAvatar({
	iconKey,
	fallbackLetter = '?',
	fallbackColor = '6b7280',
	size = 32,
	className,
	title,
}: BrandAvatarProps) {
	const known = hasBrandIcon(iconKey)
	const hex = (known ? brandHex(iconKey) : fallbackColor) ?? fallbackColor
	// Tinted background: brand colour at ~18% alpha on dark themes
	// keeps the icon legible; the foreground icon stays at full
	// brand colour for recognisability.
	const bg = `rgba(${parseInt(hex.slice(0, 2), 16)}, ${parseInt(hex.slice(2, 4), 16)}, ${parseInt(hex.slice(4, 6), 16)}, 0.18)`

	const inner = size * 0.55

	return (
		<div
			className={cn(
				'shrink-0 rounded-full flex items-center justify-center',
				className,
			)}
			style={{
				width: size,
				height: size,
				background: bg,
				color: `#${hex}`,
			}}
			aria-label={title}
			title={title}
		>
			{known ? (
				<BrandIcon iconKey={iconKey} size={inner} title={title} />
			) : (
				<span
					className="font-semibold leading-none"
					style={{ fontSize: Math.round(size * 0.45) }}
				>
					{fallbackLetter.slice(0, 1).toUpperCase()}
				</span>
			)}
		</div>
	)
}
