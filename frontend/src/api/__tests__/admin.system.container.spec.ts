import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn()
}))

vi.mock('../client', () => ({
  apiClient: { get, post }
}))

import { checkUpdates, restartService } from '@/api/admin/system'

describe('admin container update API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('exposes container update metadata', async () => {
    get.mockResolvedValue({
      data: {
        current_version: '0.1.171',
        latest_version: '0.1.172',
        has_update: true,
        cached: false,
        build_type: 'release',
        update_mode: 'container',
        update_source: '66-DASHUN/sub2api-qa'
      }
    })

    const info = await checkUpdates(true)

    expect(info.update_mode).toBe('container')
    expect(info.update_source).toBe('66-DASHUN/sub2api-qa')
  })

  it('uses the existing restart endpoint', async () => {
    post.mockResolvedValue({ data: { message: 'Service restart initiated' } })

    await restartService()

    expect(post).toHaveBeenCalledWith('/admin/system/restart')
  })
})
