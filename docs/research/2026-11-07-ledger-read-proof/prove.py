#!/usr/bin/env python3
"""Retained contract evidence, NOT production SQLite/encryption implementation.
Only synthetic fixtures. Security is checked separately by adiantum_probe.go.
Run from the repository: python3 docs/research/2026-11-07-ledger-read-proof/prove.py
"""
import argparse
import base64
import hashlib
import json
import math
import pathlib
import sqlite3
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent
C = json.loads((ROOT / 'scenarios.json').read_text())
REPORT = []


def wire(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()


def check(condition, description):
    if not condition:
        raise AssertionError(description)


def note(name, **facts):
    REPORT.append(dict(scenario=name, **facts))


def ranges(keys):
    start = end = None
    for key in keys:
        if start is None:
            start = end = key
        elif key == end + 1:
            end = key
        else:
            yield [str(start), str(end)]
            start = end = key
    if start is not None:
        yield [str(start), str(end)]


class Ledger:
    def __init__(self):
        self.db = sqlite3.connect(':memory:')
        self.db.executescript('''
          CREATE TABLE chunks(seq INTEGER PRIMARY KEY, body BLOB NOT NULL);
          CREATE TABLE rows(k TEXT PRIMARY KEY, sort INTEGER UNIQUE, seq INTEGER,
                            off INTEGER, size INTEGER, born INTEGER);
        ''')
        self.rev = 1
        self.membership_rev = 1
        self.store = 'store-incarnation-a'
        self.artifact = 'artifact-a'
        self.generation = 'generation-1'
        self.binding = 'binding-a'
        self.process = 'process-a'
        self.availability = 'present'
        self.availability_epoch = 1
        self.state = 'open'
        self.lost = self.dropped = self.unavailable = 0
        self.truncated = None
        self.next_seq = 1
        self.max_fetch = 0
        self.in_capture = False

    def meta(self):
        return dict(store=self.store, artifact=self.artifact, generation=self.generation, revision=str(self.rev),
                    state=self.state, availability=self.availability,
                    availabilityEpoch=self.availability_epoch, lost=self.lost,
                    dropped=self.dropped, unavailable=self.unavailable,
                    truncated=self.truncated)

    def put(self, items, sequences=None):
        check(not self.in_capture, 'writer cannot interleave source snapshot transaction')
        self.rev += 1
        self.membership_rev = self.rev
        bodies = []
        pending = bytearray()
        indexes = []
        pending_indexes = []
        for key, row in items:
            encoded = wire(row)
            prefix = b'{"from":' + str(key).encode() + b',"row":'
            line = prefix + encoded + b'}\n'
            if pending and len(pending) + len(line) > C['chunkTargetBytes']:
                bodies.append(bytes(pending))
                indexes.append(pending_indexes)
                pending, pending_indexes = bytearray(), []
            pending_indexes.append((key, len(pending) + len(prefix), len(encoded)))
            pending.extend(line)
        if pending:
            bodies.append(bytes(pending))
            indexes.append(pending_indexes)
        for i, (body, entries) in enumerate(zip(bodies, indexes)):
            seq = sequences[i] if sequences else self.next_seq
            self.next_seq = max(self.next_seq, seq + 1)
            self.db.execute('INSERT INTO chunks VALUES (?,?)', (seq, body))
            for key, off, size in entries:
                # Fixtures stay within SQLite signed index range; transport keys are strings.
                self.db.execute('INSERT INTO rows VALUES (?,?,?,?,?,?)',
                                (str(key), key, seq, off, size, self.rev))
        self.db.commit()

    def remove(self, predicate):
        keys = [(k,) for k, in self.db.execute('SELECT sort FROM rows') if predicate(k)]
        if keys:
            self.rev += 1
            self.membership_rev = self.rev
            self.db.executemany('DELETE FROM rows WHERE sort=?', keys)
            self.dropped += len(keys)
            self.truncated = 'cap'
            self.db.commit()

    def metadata(self, *, seal=False, lost=0, unavailable=0):
        self.rev += 1
        self.lost += lost
        self.unavailable += unavailable
        if seal:
            self.state = 'sealed'
        if lost and self.truncated is None:
            self.truncated = 'gap'

    def body_unavailable(self):
        self.rev += 1
        self.membership_rev = self.rev
        self.availability_epoch += 1
        self.availability = 'unavailable'
        self.db.execute('DELETE FROM rows')
        self.db.execute('DELETE FROM chunks')
        self.db.commit()

    def expected(self):
        return {int(k): bytes(body) for k, body in self.db.execute('''
          SELECT r.k,substr(c.body,r.off+1,r.size)
          FROM rows r JOIN chunks c USING(seq) ORDER BY r.sort
        ''')}

    def capture(self, directory, base=0, force_error=False, headroom=1 << 40, fail_after=None):
        check(base <= self.rev, 'future base revision refused')
        if force_error:
            raise OSError('fixture read failure')
        self.in_capture = True
        self.db.execute('BEGIN')
        snapshot = None
        try:
            meta = self.meta()
            membership = base < self.membership_rev
            # Reservation is deliberately conservative; production also needs a
            # physical allocation guard (SQLite max_page_count + measured file bytes).
            count, size = self.db.execute(
                'SELECT count(*),coalesce(sum(size),0) FROM rows WHERE born>?', (base,)
            ).fetchone()
            total_count = self.db.execute('SELECT count(*) FROM rows').fetchone()[0]
            reserve = 4 * size + 256 * count + 128 * total_count + (1 << 20)
            if reserve > headroom:
                raise OSError('snapshot temporary-space admission failed')
            snapshot = Snapshot(self, directory, meta, membership, reserve)
            if self.availability == 'present':
                if membership:
                    for interval in ranges(k for k, in self.db.execute('SELECT sort FROM rows ORDER BY sort')):
                        snapshot.add(dict(kind='members', interval=interval))
                for k, seq, off, size in self.db.execute(
                        'SELECT k,seq,off,size FROM rows WHERE born>? ORDER BY sort', (base,)):
                    position = 0
                    # A row too large for one page is a finite sequence of fragments,
                    # never a large exception response or a skipped row.
                    while position < size:
                        count = min(C['fragmentBytes'], size - position)
                        data = bytearray()
                        while len(data) < count:
                            n = min(C['copySliceBytes'], count - len(data))
                            piece = self.db.execute(
                                'SELECT substr(body,?,?) FROM chunks WHERE seq=?',
                                (off + position + len(data) + 1, n, seq)).fetchone()[0]
                            check(len(piece) == n, 'source row bytes exist at indexed offset')
                            self.max_fetch = max(self.max_fetch, len(piece))
                            data.extend(piece)
                        if size <= C['fragmentBytes']:
                            snapshot.add(dict(kind='row', key=k, row=json.loads(data)))
                        else:
                            snapshot.add(dict(kind='fragment', key=k, offset=position,
                                              total=size, data=base64.b64encode(data).decode()))
                        position += count
                        if fail_after is not None and snapshot.total >= fail_after:
                            raise OSError('fixture cancellation during copy')
            snapshot.finish_capture()
            return snapshot
        except BaseException:
            if snapshot is not None:
                snapshot.close()
            raise
        finally:
            self.db.rollback()
            self.in_capture = False


class Snapshot:
    def __init__(self, source, directory, meta, membership, reserve):
        self.source = source
        self.meta = meta
        self.membership = membership
        self.reserve = reserve
        self.binding = source.binding
        self.process = source.process
        self.path = pathlib.Path(directory) / f'response-{id(self)}.sqlite'
        self.db = sqlite3.connect(self.path)
        self.db.execute('CREATE TABLE records(n INTEGER PRIMARY KEY, body BLOB NOT NULL)')
        self.total = 0
        self.next = 0
        self.started = self.touched = 0
        self.expired = False
        self.max_page = 0
        self.total_wire = 0
        self.row_records = 0
        self.deadline = None

    def add(self, record):
        raw = wire(record)
        check(len(raw) < C['pageBytes'] - 4096, 'single record fits page reserve')
        self.db.execute('INSERT INTO records VALUES (?,?)', (self.total, raw))
        self.total += 1
        if record['kind'] in ('row', 'fragment'):
            self.row_records += 1

    def finish_capture(self):
        self.db.commit()
        check(self.path.stat().st_size <= self.reserve, 'spool fits reserved disk bytes')
        # Size proportional, non-renewable absolute bound; no wall-clock sleeps.
        self.deadline = min(C['leaseMaximumSeconds'], 120 + math.ceil(self.path.stat().st_size / 262144))

    def close(self):
        if not self.expired:
            self.db.close()
            self.path.unlink(missing_ok=True)
            self.expired = True

    def page(self, now=0, binding=None, process=None):
        binding = self.source.binding if binding is None else binding
        process = self.source.process if process is None else process
        if binding != self.binding or process != self.process:
            self.close()
            raise ValueError('stale binding/process cursor')
        if (self.source.artifact != self.meta['artifact'] or self.source.store != self.meta['store']
                or self.source.generation != self.meta['generation']):
            self.close()
            raise ValueError('stale artifact/store cursor')
        if self.expired or now - self.touched >= C['idleSeconds'] or now >= self.deadline:
            self.close()
            raise ValueError('cursor expired')
        if self.source.availability_epoch != self.meta['availabilityEpoch']:
            fresh = self.source.meta()
            self.close()
            return dict(kind='invalidated', metadata=fresh, binding=self.binding)
        envelope = dict(kind='page', metadata=self.meta, binding=self.binding,
                        membership=self.membership, ordinal=self.next, records=[], done=False)
        for ordinal, raw in self.db.execute('SELECT n,body FROM records WHERE n>=? ORDER BY n', (self.next,)):
            item = json.loads(raw)
            proposed = dict(envelope, records=envelope['records'] + [item])
            if len(envelope['records']) >= C['pageRows'] or len(wire(proposed)) > C['pageBytes'] - 512:
                break
            envelope['records'].append(item)
            self.next = ordinal + 1
        check(self.next == self.total or envelope['records'], 'non-final page makes strict finite progress')
        envelope['done'] = self.next == self.total
        envelope['next'] = self.next
        encoded = len(wire(dict(jsonrpc='2.0', id='fixture', result=envelope)))
        check(encoded <= C['pageBytes'], 'encoded whole response <=64KiB')
        check(len(envelope['records']) <= C['pageRows'], 'at most256 row/manifest/fragment records')
        self.max_page = max(self.max_page, encoded)
        self.total_wire += encoded
        self.touched = now
        return envelope


class Client:
    def __init__(self, binding='binding-a'):
        self.binding = binding
        self.artifact = 'artifact-a'
        self.generation = 'generation-1'
        self.store = 'store-incarnation-a'
        self.rev = 0
        self.rows = {}
        self.meta = None
        self.pending = None
        self.barrier = 0

    def invalidate(self, meta):
        revision = int(meta['revision'])
        self.barrier = max(self.barrier, revision)
        self.pending = None
        if meta['availability'] != 'present':
            self.rows = {}
            self.rev = revision
            self.meta = meta

    def accept(self, page):
        try:
            return self._accept(page)
        except Exception:
            self.pending = None
            raise

    def _accept(self, page):
        if page['binding'] != self.binding:
            return False
        if (page['metadata']['artifact'] != self.artifact or page['metadata']['store'] != self.store
                or page['metadata']['generation'] != self.generation):
            return False
        rev = int(page['metadata']['revision'])
        if rev < max(self.rev, self.barrier):
            return False
        if page['kind'] == 'invalidated':
            self.invalidate(page['metadata'])
            return False
        if self.pending is None:
            check(page['ordinal'] == 0, 'new assembly starts at zero')
            self.pending = dict(rev=rev, next=0, intervals=[], changed={}, fragment=None)
        p = self.pending
        check(p['rev'] == rev and p['next'] == page['ordinal'], 'ordered same-revision assembly')
        for item in page['records']:
            kind = item['kind']
            if kind == 'members':
                check(page['membership'], 'unchanged membership cannot carry a replacement interval')
                lo, hi = map(int, item['interval'])
                check(0 <= lo <= hi, 'manifest interval has ordered nonnegative endpoints')
                check(not p['intervals'] or p['intervals'][-1][1] + 1 < lo,
                      'manifest intervals are sorted, disjoint and maximal')
                p['intervals'].append((lo, hi))
            elif kind == 'row':
                key = int(item['key'])
                check(key not in p['changed'], 'changed row keys are unique')
                p['changed'][key] = wire(item['row'])
            elif kind == 'fragment':
                if p['fragment'] is None:
                    check(item['offset'] == 0, 'fragment starts at zero')
                    p['fragment'] = (item['key'], item['total'], bytearray())
                key, total, data = p['fragment']
                check(key == item['key'] and total == item['total'] and len(data) == item['offset'],
                      'fragment identity, total and byte offset remain exact')
                data.extend(base64.b64decode(item['data'], validate=True))
                if len(data) == total:
                    json.loads(data)  # No partial row may be parsed or published.
                    check(int(key) not in p['changed'], 'changed row keys are unique')
                    p['changed'][int(key)] = bytes(data)
                    p['fragment'] = None
        p['next'] = page['next']
        if page['done']:
            check(p['fragment'] is None, 'no partial row at atomic install')
            if page['metadata']['availability'] != 'present':
                result = {}
            elif page['membership']:
                check(all(any(lo <= key <= hi for lo, hi in p['intervals'])
                          for key in p['changed']), 'changed row key is outside the authoritative manifest')
                result = {k: v for k, v in self.rows.items()
                          if any(lo <= k <= hi for lo, hi in p['intervals'])}
                result.update(p['changed'])
                wanted = sum(hi - lo + 1 for lo, hi in p['intervals'])
                check(len(result) == wanted, 'complete manifest has every row, no invented hole')
            else:
                check(p['changed'].keys() <= self.rows.keys(), 'unchanged membership requires every base row')
                result = dict(self.rows)
                result.update(p['changed'])
            self.rows, self.rev, self.meta = result, rev, page['metadata']
            self.pending = None
        return True


class ReadOwner:
    """Finite owner/timer model; advance_time is the independent scheduler event."""
    def __init__(self):
        self.leases = {}

    def begin(self, ledger, directory, now=0):
        key = (ledger.binding, ledger.artifact)
        if key in self.leases:
            return self.leases[key]
        if len(self.leases) == 4:
            return None  # Waiting, not a busy refusal and not an automatic retry.
        result = ledger.capture(directory)
        result.started = result.touched = now
        result.deadline += now
        self.leases[key] = result
        return result

    def release(self, ledger):
        self.leases.pop((ledger.binding, ledger.artifact)).close()

    def advance_time(self, now):
        # Invoked by the owner clock, without requiring next/cancel/begin.
        for key, lease in list(self.leases.items()):
            if lease.expired or now >= lease.touched + C['idleSeconds'] or now >= lease.deadline:
                del self.leases[key]
                lease.close()


def drain(snapshot, client, mutate=None):
    pages = 0
    installed = client.rows
    while True:
        page = snapshot.page(now=pages / 100)
        if mutate:
            mutate(pages)
        accepted = client.accept(page)
        pages += 1
        if page.get('done'):
            break
        check(client.rows is installed, 'pages never partially replace installed immutable projection')
        check(accepted, 'ordinary writer mutations never invalidate snapshot')
        check(pages <= snapshot.total + 1, 'finite record-bound progress without restart')
    snapshot.close()
    return pages


def main(output_path):
    with tempfile.TemporaryDirectory(prefix='nocx-ledger-proof-') as tmp:
        for cap, count in [(C['defaultCapBytes'], C['defaultRows']),
                           (C['maximumSettingCapBytes'], C['maximumRows'])]:
            ledger = Ledger()
            ledger.put((i, {'text': 'x' * 79}) for i in range(count))
            client = Client()
            expected = ledger.expected()
            snap = ledger.capture(tmp)
            revision = ledger.rev
            def mutation(page):
                k = count + page
                ledger.put([(k, {'text': 'tail' * 20})])
                ledger.remove(lambda i: 10 <= i < min(count - 10, 10 + 10 * (page + 1)))
            pages = drain(snap, client, mutation)
            check(client.rows == expected and client.rev == revision,
                  'continuous append+eviction finishes exact captured snapshot')
            refreshed = ledger.capture(tmp, client.rev)
            final = ledger.expected()
            drain(refreshed, client)
            check(client.rows == final, 'next finite delta removes old middle and adds latest tail')
            check(sum(len(v) for v in expected.values()) < cap, 'realistic fixture fits configured body cap')
            note('continuous-append-eviction', configuredCap=cap, rows=count, pages=pages,
                 maxEncodedPage=snap.max_page, restarts=0)

        ledger = Ledger()
        ledger.put([(100, {'text': 'tail'})], [1000000])
        client = Client()
        drain(ledger.capture(tmp), client)
        old = dict(client.rows)
        ledger.put([(90, {'text': 'head'}), (91, {'text': 'head-2'})], [-17])
        ledger.put([(95, {'text': 'zero-chunk'})], [0])
        ledger.put([(97, {'text': 'sparse'})], [27])
        ledger.remove(lambda i: i == 95)
        snap = ledger.capture(tmp, client.rev)
        drain(snap, client)
        check(max(client.rows) == 100 and set(client.rows) == {90, 91, 97, 100},
              'prepend with unchanged maximum, negative/zero/sparse chunks and holes')
        check(old == {100: wire({'text': 'tail'})}, 'old pinned snapshot remains immutable')
        note('prepend-sparse-protected-ends', survivors=sorted(client.rows))

        prior_body = dict(client.rows)
        ledger.metadata(lost=7, unavailable=3)
        ledger.metadata(seal=True)
        snap = ledger.capture(tmp, client.rev)
        check(not snap.membership and snap.row_records == 0, 'metadata-only delta hauls no row bodies/manifest')
        drain(snap, client)
        check(client.rows == prior_body and client.meta['state'] == 'sealed'
              and client.meta['lost'] == 7 and client.meta['unavailable'] == 3,
              'seal and causal loss accepted without row advance')
        note('metadata-only-seal-loss', revision=client.rev, bodies=0)

        ledger = Ledger()
        ledger.put([(0, {'text': 'closing screen'})])
        ledger.metadata(seal=True)
        client = Client()
        snap = ledger.capture(tmp)
        drain(snap, client)
        check(client.rows == ledger.expected() and client.meta['state'] == 'sealed',
              'final retained closing rows and seal are installed together')
        note('closing-row', rows=len(client.rows))

        ledger = Ledger()
        client = Client()
        ledger.metadata(seal=True)
        drain(ledger.capture(tmp), client)
        check(client.rows == {} and client.meta['availability'] == 'present', 'empty is present')
        ledger.body_unavailable()
        drain(ledger.capture(tmp, client.rev), client)
        check(client.rows == {} and client.meta['availability'] == 'unavailable', 'unavailable is not empty')
        for state in ('entry-missing', 'artifact-missing', 'not-recorded'):
            ledger.availability = state
            ledger.availability_epoch += 1
            ledger.rev += 1
            drain(ledger.capture(tmp, client.rev), client)
            check(client.meta['availability'] == state, 'absence states remain distinct')
        before = client.meta
        try:
            ledger.capture(tmp, force_error=True)
            raise AssertionError('read failure must not answer empty')
        except OSError:
            check(client.meta is before, 'read failure keeps last accepted state')
        note('availability-algebra', states=['present-empty', 'unavailable', 'entry-missing',
                                            'artifact-missing', 'not-recorded', 'read-error'])

        ledger = Ledger()
        ledger.put((i, {'text': str(i)}) for i in range(600))
        client = Client()
        snap = ledger.capture(tmp)
        late = snap.page()
        ledger.body_unavailable()
        invalidation = snap.page()
        client.accept(invalidation)
        check(not client.accept(late) and not client.rows, 'late pages cannot resurrect unavailable output')
        note('unavailable-during-read', latePageRejected=True)

        ledger = Ledger()
        styled = {'text': 'A界e\u0301👩\u200d💻 ', 'marks': [[1, 1, 2], [2, 2, 1], [3, 3, 2]],
                  'runs': [[[0, 66051 + 257, 1, 0, 0], 7]], 'wrap': True, 'continuation': True}
        ledger.put([(0, styled), (1, {'text': 'z' * C['oversizedTextBytes']})])
        client = Client()
        expected = ledger.expected()
        snap = ledger.capture(tmp)
        pages = drain(snap, client)
        check(client.rows == expected, 'oversized row is exact, neither skipped nor truncated')
        check(ledger.max_fetch <= C['copySliceBytes'], 'server source byte slices remain16KiB')
        note('oversized-row-and-cell-fidelity', oversizedBytes=C['oversizedTextBytes'],
             pages=pages, maxEncodedPage=snap.max_page, maxSourceFetch=ledger.max_fetch,
             digest=hashlib.sha256(client.rows[1]).hexdigest())

        ledger = Ledger()
        ledger.put([(0, {'text': 'bound'})])
        for mutation, message in [('binding', 'stale binding'), ('process', 'restart cursor'),
                                  ('idle', 'idle expiry'), ('absolute', 'absolute expiry')]:
            snap = ledger.capture(tmp)
            old_binding, old_process = ledger.binding, ledger.process
            if mutation == 'binding':
                ledger.binding = 'binding-new'
            if mutation == 'process':
                ledger.process = 'process-new'
            now = 31 if mutation == 'idle' else 0
            if mutation == 'absolute':
                snap.touched = snap.deadline
                now = snap.deadline + 1
            try:
                snap.page(now=now)
                raise AssertionError(message + ' must refuse cursor')
            except ValueError:
                check(not snap.path.exists(), 'expired cursor releases spool')
            ledger.binding, ledger.process = old_binding, old_process
        snap = ledger.capture(tmp)
        old_page = snap.page()
        rebound = Client('binding-new')
        check(not rebound.accept(old_page), 'client rejects old binding reply')
        snap.close()
        check(ledger.state == 'open', 'restart/disconnect never seals block')
        note('restart-binding-expiry', cleanup=True, blockState=ledger.state)

        for field in ('artifact', 'store', 'generation'):
            snap = ledger.capture(tmp)
            previous = getattr(ledger, field)
            setattr(ledger, field, previous + '-replaced')
            try:
                snap.page()
                raise AssertionError('replaced identity must invalidate cursor')
            except ValueError:
                check(not snap.path.exists(), 'identity replacement removes response')
            setattr(ledger, field, previous)
        malformed = ledger.capture(tmp)
        bad_page = malformed.page()
        bad_page['ordinal'] = 1
        unchanged = Client()
        try:
            unchanged.accept(bad_page)
            raise AssertionError('out-of-order page must be refused')
        except AssertionError as error:
            check(str(error) == 'new assembly starts at zero', 'named malformed-page refusal')
            check(not unchanged.rows and unchanged.rev == 0, 'malformed page cannot install a partial view')
        malformed.close()
        note('identity-and-malformed-page', artifactReplacement=True, storeReplacement=True)

        try:
            ledger.capture(tmp, fail_after=1)
            raise AssertionError('mid-copy cancellation must abort')
        except OSError:
            check(not ledger.in_capture, 'cancellation releases source transaction')
            check(not list(pathlib.Path(tmp).glob('response-*')), 'cancellation removes partial spool')
        note('capture-cancellation', cleanup=True)

        try:
            ledger.capture(tmp, headroom=1)
            raise AssertionError('disk reservation must fail before capture')
        except OSError:
            check(ledger.expected() == {0: wire({'text': 'bound'})}, 'space error changes no source rows')
        check(not list(pathlib.Path(tmp).glob('response-*')), 'all completed/expired response spools removed')
        note('disk-pressure-cleanup', durableRowsUntouched=True)

        owner = ReadOwner()
        sources = [Ledger() for _ in range(5)]
        for i, source in enumerate(sources):
            source.binding = f'view-{i}'
            source.put([(0, {'text': f'view-{i}'})])
        admitted = [owner.begin(source, tmp) for source in sources[:4]]
        check(owner.begin(sources[0], tmp) is admitted[0], 'same-key readers share one response')
        check(owner.begin(sources[4], tmp) is None, 'fifth read waits without capturing or busy refusal')
        check(admitted[0].page()['done'], 'continuation progresses while all four slots are occupied')
        owner.release(sources[0])
        check(owner.begin(sources[4], tmp) is not None and len(owner.leases) == 4,
              'waiting read admitted after slot release, never a fifth lease')
        for source in sources[1:]:
            owner.release(source)
        check(not list(pathlib.Path(tmp).glob('response-*')), 'all owner leases release private files')
        note('four-lease-admission', maximum=4, coalesced=True, waitingThenAdmitted=True)

        stalled = ReadOwner()
        dormant = [stalled.begin(source, tmp) for source in sources[:4]]
        check(stalled.begin(sources[4], tmp) is None, 'four dormant leases initially occupy admission')
        paths = [lease.path for lease in dormant]
        stalled.advance_time(C['idleSeconds'])
        check(not stalled.leases and all(not path.exists() for path in paths),
              'owner timer alone removes dormant files and registrations')
        admitted_later = stalled.begin(sources[4], tmp, now=C['idleSeconds'])
        check(admitted_later is not None, 'fifth reader proceeds after autonomous expiry')
        admitted_later.touched = admitted_later.deadline - 1
        stalled.advance_time(admitted_later.deadline)
        check(not stalled.leases and not admitted_later.path.exists(),
              'absolute owner expiry reclaims a recently active lease without client traffic')
        note('owner-driven-expiry', nextOrCancelCalls=0, reclaimedIdleLeases=4,
             fifthReadAdmitted=True, absoluteExpiryReclaimed=True)

        malformed_source = Ledger()
        malformed_source.put([(9, {'text': 'installed'})])
        preserved = Client()
        drain(malformed_source.capture(tmp), preserved)
        installed_rows, installed_meta, installed_rev = preserved.rows, preserved.meta, preserved.rev
        malformed_source.remove(lambda key: key == 9)
        malformed_source.put([(1, {'text': 'one'}), (2, {'text': 'two'})])
        for variant in ('outside', 'reversed', 'overlapping', 'unsorted', 'incomplete'):
            malformed_snapshot = malformed_source.capture(tmp)
            bad = malformed_snapshot.page()
            check(bad['done'], 'malformed fixture fits one complete page')
            if variant == 'outside':
                next(item for item in bad['records'] if item.get('key') == '2')['key'] = '3'
            elif variant == 'reversed':
                bad['records'][0]['interval'] = ['2', '1']
            elif variant == 'overlapping':
                bad['records'].append(dict(kind='members', interval=['2', '2']))
            elif variant == 'unsorted':
                bad['records'][0]['interval'] = ['2', '2']
                bad['records'].append(dict(kind='members', interval=['1', '1']))
            else:
                bad['records'] = [item for item in bad['records'] if item.get('key') != '2']
            bad['next'] = len(bad['records'])
            rejected = False
            try:
                preserved.accept(bad)
            except AssertionError:
                rejected = True
            check(rejected, f'{variant} manifest must be refused')
            check(preserved.rows is installed_rows and preserved.meta is installed_meta
                  and preserved.rev == installed_rev and preserved.pending is None,
                  'malformed membership preserves installed rows/metadata/revision and discards staging')
            malformed_snapshot.close()
        note('exact-manifest-validation', rejected=['outside', 'reversed', 'overlapping', 'unsorted', 'incomplete'],
             installedSnapshotPreserved=True)

        sizes = {str(n): len(wire({'from': 0, 'row': {'text': 'x' * n}})) + 1
                 for n in C['plainWidths']}
        note('fixture-size-measurement', jsonlBytes=sizes)
    result = dict(kind='contract-model-only', scenarios=REPORT,
                  productionEncryptedStoreExercised=False,
                  productionFrontendExercised=False)
    output_path.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))
    print('PASS: all ledger contract model assertions')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=pathlib.Path,
                        default=pathlib.Path('/tmp/nocx-ledger-model-results.json'))
    main(parser.parse_args().output)
