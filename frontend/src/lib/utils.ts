import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

export type WithElementRef<T> = T & {
	ref?: HTMLElement | null;
}

export type { WithoutChild, WithoutChildrenOrChild } from "bits-ui";

/** Returns '#000' or '#fff' for readable text on the given hex background. */
export function contrastColor(hex: string): string {
	const r = parseInt(hex.slice(1, 3), 16);
	const g = parseInt(hex.slice(3, 5), 16);
	const b = parseInt(hex.slice(5, 7), 16);
	return (r * 0.299 + g * 0.587 + b * 0.114) > 140 ? '#000' : '#fff';
}