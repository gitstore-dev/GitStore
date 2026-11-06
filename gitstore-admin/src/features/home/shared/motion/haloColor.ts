export function haloColor(value: string): [number, number, number] {
  const hex = value.trim().match(/^#([0-9a-f]{6})$/i)
  if (hex)
    return [0, 2, 4].map(
      (i) => parseInt(hex[1]!.slice(i, i + 2), 16) / 255,
    ) as [number, number, number]
  const rgb = value.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/)
  if (rgb)
    return rgb.slice(1, 4).map((v) => Number(v) / 255) as [
      number,
      number,
      number,
    ]
  return [0.36, 0.46, 0.4]
}
