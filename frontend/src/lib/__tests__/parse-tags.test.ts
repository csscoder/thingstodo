import { describe, expect, it } from 'vitest'
import { parseTitleAndTags, parseTitleTokens } from '../parse-tags'

describe('parseTitleAndTags', () => {
  it('parses ASCII tags with hyphens', () => {
    expect(parseTitleAndTags('Buy milk #to-do')).toEqual({
      title: 'Buy milk',
      tagNames: ['to-do'],
    })
  })

  it('parses Cyrillic tags', () => {
    expect(parseTitleAndTags('Купить молоко #покупки')).toEqual({
      title: 'Купить молоко',
      tagNames: ['покупки'],
    })
  })

  it('parses accented tags with digits and hyphens', () => {
    expect(parseTitleAndTags('Buy #café-2 now')).toEqual({
      title: 'Buy now',
      tagNames: ['café-2'],
    })
  })

  // Pins current behavior: a #token glued to an email/URL-like string is a tag.
  it('treats #anchor in email@x.com#anchor as a tag', () => {
    expect(parseTitleAndTags('email@x.com#anchor')).toEqual({
      title: 'email@x.com',
      tagNames: ['anchor'],
    })
  })
})

describe('parseTitleTokens', () => {
  it('matches the exact project name when a longer name shares the prefix', () => {
    expect(parseTitleTokens('Fix $Work today', ['Workshop', 'Work'])).toEqual({
      title: 'Fix today',
      tagNames: [],
      projectRef: 'Work',
    })
  })

  it('does not match a project name that continues with letters', () => {
    expect(parseTitleTokens('Fix $Workshop today', ['Work'])).toEqual({
      title: 'Fix $Workshop today',
      tagNames: [],
      projectRef: null,
    })
  })

  it('matches nothing on an incomplete prefix', () => {
    expect(parseTitleTokens('Fix $Wor today', ['Workshop', 'Work'])).toEqual({
      title: 'Fix $Wor today',
      tagNames: [],
      projectRef: null,
    })
  })
})
