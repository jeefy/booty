import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import type { RouteLocationNormalized } from 'vue-router'
import { MAIN_CONTENT_ID, focusMainAfterNavigation } from '@/router'

function route(path: string, matched = true): RouteLocationNormalized {
  return {
    path,
    matched: matched ? [{}] : [],
    fullPath: path,
    hash: '',
    query: {},
    params: {},
    name: undefined,
    meta: {},
    redirectedFrom: undefined
  } as unknown as RouteLocationNormalized
}

describe('focusMainAfterNavigation', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  function mountMain(): HTMLElement {
    document.body.innerHTML = `<button id="nav">nav</button><main id="${MAIN_CONTENT_ID}" tabindex="-1"></main>`
    const nav = document.getElementById('nav')
    nav?.focus()
    const main = document.getElementById(MAIN_CONTENT_ID)
    if (!main) throw new Error('no main')
    return main
  }

  it('focuses main after navigating between pages', async () => {
    const main = mountMain()
    focusMainAfterNavigation(route('/hosts'), route('/'))
    await nextTick()
    expect(document.activeElement).toBe(main)
  })

  it('leaves focus alone on the initial navigation and same-path changes', async () => {
    const main = mountMain()
    focusMainAfterNavigation(route('/hosts'), route('/', false))
    focusMainAfterNavigation(route('/hosts'), route('/hosts'))
    await nextTick()
    expect(document.activeElement).not.toBe(main)
  })
})
