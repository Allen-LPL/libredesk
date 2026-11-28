// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import View from './CreateOrEditAssistant.vue'
import api from '@/api'
const { form, push, emit } = vi.hoisted(() => ({ form: {}, push: vi.fn(), emit: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push }), RouterLink: { render: () => null } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (k) => k }) }))
vi.mock('@/api', () => ({
  default: {
    getAIAssistant: vi.fn(),
    getAIAssistantStats: vi.fn(),
    createAIAssistant: vi.fn(),
    updateAIAssistant: vi.fn(),
    replaceAIAssistantKnowledgeItems: vi.fn()
  }
}))
vi.mock('@/stores/aiAssistant', () => ({ useAIAssistantStore: () => ({ invalidate: vi.fn() }) }))
vi.mock('@/features/admin/ai/AssistantForm.vue', () => ({
  default: {
    props: ['submitForm'],
    setup(p) {
      form.props = p
      return () => h('div')
    }
  }
}))
vi.mock('@shared-ui/components/ui/breadcrumb', () => ({ CustomBreadcrumb: { render: () => null } }))
vi.mock('@shared-ui/utils/http.js', () => ({ handleHTTPError: (e) => e }))
let app
async function mount(id) {
  api.getAIAssistant.mockResolvedValue({ data: { data: { id } } })
  api.getAIAssistantStats.mockResolvedValue({ data: { data: {} } })
  app = createApp(View, { id })
  app.config.globalProperties.emitter = { emit }
  app.config.globalProperties.$t = (k) => k
  app.mount(document.createElement('div'))
  await nextTick()
  await vi.waitFor(() => expect(form.props).toBeDefined())
}
afterEach(() => {
  app?.unmount()
  delete form.props
  vi.clearAllMocks()
})
it('preserves grants on an ordinary save and does not replace if profile save fails', async () => {
  await mount('4')
  await form.props.submitForm(new FormData(), undefined)
  expect(api.updateAIAssistant).toHaveBeenCalledOnce()
  expect(api.replaceAIAssistantKnowledgeItems).not.toHaveBeenCalled()
  api.updateAIAssistant.mockRejectedValueOnce(new Error('profile error'))
  expect(await form.props.submitForm(new FormData(), [3])).toBe(false)
  expect(api.replaceAIAssistantKnowledgeItems).not.toHaveBeenCalled()
})
it('retains new identity after a partial save; retries without creating a second assistant', async () => {
  await mount()
  api.createAIAssistant.mockResolvedValue({ data: { data: { id: 8 } } })
  api.replaceAIAssistantKnowledgeItems.mockRejectedValueOnce(new Error('failed'))
  expect(await form.props.submitForm(new FormData(), [3])).toBe(false)
  expect(emit.mock.calls.at(-1)[1].description).toContain('admin.ai.assistant.knowledgeSaveError')
  expect(push).not.toHaveBeenCalled()
  expect(await form.props.submitForm(new FormData(), [3])).toBe(true)
  expect(api.createAIAssistant).toHaveBeenCalledOnce()
  expect(api.replaceAIAssistantKnowledgeItems.mock.calls).toEqual([
    [8, [3]],
    [8, [3]]
  ])
  expect(push).toHaveBeenCalledOnce()
})
