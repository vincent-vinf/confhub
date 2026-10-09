import { expect, it } from 'vitest'
import { formatContent, validateContent } from './format'

it.each([
  ['json', '{broken'],
  ['yaml', 'a: [1'],
  ['toml', 'a = ['],
  ['xml', '<a>'],
  ['properties', 'a=\\u00XX'],
  ['ini', '[broken'],
] as const)('%s 错误阻止确认', async (format, content) => {
  expect(await validateContent(format, content)).toContain('语法错误')
})
it.each([
  ['text', ''],
  ['json', '{"a":1}'],
  ['yaml', 'a: 1\n---\nb: 2'],
  ['toml', 'a = 1'],
  ['xml', '<a/>'],
  ['properties', 'a=\\u4e2d'],
  ['ini', '[main]\na=1'],
] as const)('%s 合法内容不自动改写', async (format, content) => {
  expect(await validateContent(format, content)).toBeUndefined()
})
it('格式化 YAML 保留多文档和注释', async () => {
  const formatted = await formatContent('yaml', '# first\na: [1,2]\n---\nb: 2\n')
  expect(formatted).toContain('# first')
  expect(formatted).toContain('---')
  const { parseAllDocuments } = await import('yaml')
  expect(parseAllDocuments(formatted).map((d) => d.toJSON())).toEqual([{ a: [1, 2] }, { b: 2 }])
})
it('JSON 格式化保留值且报错不返回破坏后的内容', async () => {
  expect(await formatContent('json', '{"a":1}')).toBe('{\n  "a": 1\n}\n')
  await expect(formatContent('json', '{broken')).rejects.toThrow()
  expect(await validateContent('text', 'a'.repeat(1048577))).toContain('1 MiB')
})
