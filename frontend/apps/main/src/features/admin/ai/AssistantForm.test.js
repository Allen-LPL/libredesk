// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import AssistantForm from './AssistantForm.vue'
import api from '@/api'

vi.mock('@/api', () => ({
  default: {
    getAITools: vi.fn(),
    getTeamsCompact: vi.fn(),
    getAISnippets: vi.fn(),
    getAIAssistantKnowledgeItems: vi.fn()
  }
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key) => key }) }))
let app
afterEach(() => {
  app?.unmount()
  document.body.replaceChildren()
  vi.clearAllMocks()
})
const resp = (data) => ({ data: { data } })
async function mount() {
  for (const method of ['getAITools', 'getTeamsCompact']) api[method].mockResolvedValue(resp([]))
  api.getAISnippets.mockResolvedValue(
    resp([
      { id: 3, title: 'Selected source', content: 'hidden body', enabled: false },
      { id: 7, title: 'Other', enabled: true }
    ])
  )
  const submitForm = vi.fn().mockResolvedValue(true)
  const el = document.createElement('div')
  document.body.append(el)
  app = createApp(AssistantForm, {
    initialValues: { id: 1, name: 'Bot' },
    isEditing: true,
    submitForm
  })
  app.config.globalProperties.emitter = { emit: vi.fn() }
  app.config.globalProperties.$t = (k) => k
  app.mount(el)
  await nextTick()
  const save = async () => {
    const n = submitForm.mock.calls.length
    el.querySelector('form').dispatchEvent(new Event('submit', { cancelable: true }))
    await vi.waitFor(() => expect(submitForm).toHaveBeenCalledTimes(n + 1))
    return submitForm.mock.lastCall
  }
  return { el, save }
}
it('preserves unchanged grants and explicitly clears a disabled grant', async () => {
  api.getAIAssistantKnowledgeItems.mockResolvedValue(resp({ knowledge_item_ids: [3] }))
  const { el, save } = await mount()
  expect(el.textContent).toContain('Selected source')
  expect(el.textContent).not.toContain('hidden body')
  expect(el.textContent).toContain('globals.terms.disabled')
  const box = el.querySelector('input[value="3"]')
  expect(box.checked).toBe(true)
  box.click()
  await nextTick()
  box.click()
  await nextTick()
  expect((await save())[1]).toBeUndefined()
  box.click()
  const [form, ids] = await save()
  expect(ids).toEqual([])
  expect(JSON.parse(form.get('data'))).not.toHaveProperty('knowledge_item_ids')
  expect((await save())[1]).toBeUndefined()
})
it('never clears on failed load and can retry', async () => {
  api.getAIAssistantKnowledgeItems
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue(resp({ knowledge_item_ids: [3] }))
  const { el, save } = await mount()
  expect(el.textContent).toContain('admin.ai.assistant.knowledgeLoadError')
  expect((await save())[1]).toBeUndefined()
  el.querySelector('fieldset button').click()
  await vi.waitFor(() => expect(el.querySelector('input[value="3"]')?.checked).toBe(true))
})
