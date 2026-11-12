import type { Row } from './generated/session.frame'

/** Compare the compact facts used by live snapshots and stored-row paint. */
export function sameRow(a: Row, b: Row): boolean {
  if (a.text !== b.text || a.wrap !== b.wrap || a.continuation !== b.continuation) return false
  if ((a.marks === undefined) !== (b.marks === undefined)) return false
  if ((a.marks?.length ?? 0) !== (b.marks?.length ?? 0)) return false
  for (let index = 0; index < (a.marks?.length ?? 0); index++) {
    const left = a.marks![index]
    const right = b.marks![index]
    if (left[0] !== right[0] || left[1] !== right[1] || left[2] !== right[2]) return false
  }
  if ((a.runs === undefined) !== (b.runs === undefined)) return false
  if ((a.runs?.length ?? 0) !== (b.runs?.length ?? 0)) return false
  for (let index = 0; index < (a.runs?.length ?? 0); index++) {
    const [left, length] = a.runs![index]
    const [right, otherLength] = b.runs![index]
    if (length !== otherLength) return false
    if (left === 0 || right === 0) {
      if (left !== right) return false
    } else {
      if (left.length !== right.length) return false
      for (let field = 0; field < left.length; field++) {
        if (left[field] !== right[field]) return false
      }
    }
  }
  return true
}

/** Own comparison facts so later mutation of a received frame cannot change
 * equality with the installed snapshot or a painted stored row. */
export function ownRow(row: Row): Row {
  return {
    ...row,
    marks: row.marks?.map((mark) => [...mark]),
    runs: row.runs?.map(([style, length]) => [style === 0 ? 0 : [...style], length]),
  }
}
