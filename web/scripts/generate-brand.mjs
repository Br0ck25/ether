// Shared vector geometry is also consumed by React's brand component.
// Run from the repository root: node web/scripts/generate-brand.mjs
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const art = JSON.parse(await readFile(path.join(root, 'web/src/lib/brand-art.json'), 'utf8'));
const css = `
 .ether-ink{stroke:var(--ink)}.ether-accent{stroke:var(--accent)}
 .ether-trace{stroke-dasharray:1;stroke-dashoffset:0}
 [data-motion=animated] .ether-trace{animation:ether-draw .8s cubic-bezier(.22,.68,.16,1) var(--delay,0s) backwards}
 [data-motion=animated] .ether-signal{animation:ether-draw .8s cubic-bezier(.22,.68,.16,1) var(--delay,0s) backwards,ether-breathe 5s ease-in-out 2s infinite}
 @keyframes ether-draw{from{stroke-dashoffset:1}}
 @keyframes ether-breathe{0%,20%,70%,100%{opacity:1}45%{opacity:.5}}
 @media(prefers-reduced-motion:reduce){.ether-trace,.ether-signal{animation:none!important}}`;
function svg(tone, { mark = false, animated = false, appIcon = false, autoTheme = false } = {}) {
 const width = mark ? 128 : 510;
 const box = mark && !appIcon ? '16 16 96 96' : `0 0 ${width} 128`;
 const theme = autoTheme ? `@media(prefers-color-scheme:dark){.ether-logo{--ink:${art.ink.dark}!important;--accent:${art.accent.dark}!important}}` : '';
 const paths = art.mark.map(p => `<path d="${p.d}" pathLength="1" stroke-width="${p.width}" class="ether-trace ether-${p.color}${p.pulse ? ' ether-signal' : ''}" style="--delay:${p.delay}s"/>`).join('');
 const word = art.word.map(p => `<path d="${p.d}" transform="translate(${p.x} 0)" pathLength="1" class="ether-trace ether-ink" style="--delay:${p.delay}s"/>`).join('');
 return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${box}" width="${width}" height="128" role="img" aria-label="Ether" class="ether-logo" data-motion="${animated ? 'animated' : 'static'}" style="--ink:${art.ink[tone]};--accent:${art.accent[tone]}">
<title>Ether</title><desc>Continuum: a continuous current folded into an e.</desc>
<style>${css}${theme}</style>
${appIcon ? '<rect width="128" height="128" rx="28" fill="#0f172a"/>' : ''}
<g transform="translate(16 16)" fill="none" stroke-linecap="round" stroke-linejoin="round">${paths}</g>
${mark ? '' : `<g transform="translate(142 23) scale(1.32)" fill="none" stroke-width="7" stroke-linecap="round" stroke-linejoin="round">${word}</g>`}
</svg>\n`;
}
await mkdir(path.join(root, '.github/assets'), { recursive: true });
await mkdir(path.join(root, 'web/public/brand'), { recursive: true });
for (const tone of ['light', 'dark']) {
 await writeFile(path.join(root, '.github/assets', tone === 'light' ? 'ether-logo.svg' : 'ether-logo-dark.svg'), svg(tone, { animated: true }));
 await writeFile(path.join(root, 'web/public/brand', `ether-${tone}.svg`), svg(tone));
 await writeFile(path.join(root, 'web/public/brand', `ether-mark-${tone}.svg`), svg(tone, { mark: true }));
}
await writeFile(path.join(root, 'web/public/favicon.svg'), svg('light', { mark: true, autoTheme: true }));
await writeFile(path.join(root, 'web/public/brand/ether-app-icon.svg'), svg('dark', { mark: true, appIcon: true }));
