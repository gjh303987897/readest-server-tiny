import { useEffect, useRef, useState } from 'react';

export function formatBytes(bytes: number) {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** index;
  return `${value >= 100 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

export function useReducedMotion() {
  const [reduced, setReduced] = useState(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  useEffect(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)');
    const update = () => setReduced(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, []);
  return reduced;
}

export function AnimatedMetric({ value, format = (number: number) => Math.round(number).toLocaleString('zh-CN') }: { value?: number; format?: (number: number) => string }) {
  const reduced = useReducedMotion();
  const current = useRef(0);
  const [display, setDisplay] = useState(0);
  useEffect(() => {
    if (value === undefined) return;
    if (reduced) { current.current = value; return; }
    const from = current.current;
    const started = performance.now();
    let frame: number;
    const tick = (now: number) => {
      const progress = Math.min((now - started) / 650, 1);
      current.current = from + (value - from) * (1 - (1 - progress) ** 3);
      setDisplay(current.current);
      if (progress < 1) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [value, reduced]);
  // Assistive technology receives the final value without announcing every frame.
  return <strong aria-label={value === undefined ? '正在加载' : format(value)}><span aria-hidden="true">{value === undefined ? '—' : format(reduced ? value : display)}</span></strong>;
}
