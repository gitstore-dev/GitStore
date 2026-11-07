import { describe, expect, it } from 'vitest'
import { addTags } from './TagInput'
describe('tag tokens', () => {
  it('splits pasted lists, trims empty entries and preserves unique tag ordering', () => {
    expect(
      addTags(['ceramics'], 'homeware, ceramics,\n hand made, , '),
    ).toEqual(['ceramics', 'homeware', 'hand made'])
    expect(addTags(['Ceramics'], 'ceramics')).toEqual(['Ceramics', 'ceramics'])
    expect(addTags([], ', \n ')).toEqual([])
  })
})
