/**
 * What a place is running, and what it is being taken off.
 *
 * The first test is the one that matters: its figures were copied out of what
 * `/deploy-current` answered for a real place on a real cluster, one pod, two containers,
 * the rollout long since finished. Before the comparison was made against the workload's
 * set of images rather than against one of them, that answer put this workload on its
 * badge as retiring its own second container, and would have gone on doing so.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

import {
  comingOn,
  podsOn,
  primaryImage,
  replacing,
  replacingPods,
  servedBy,
  wantedPods,
  wantedTags,
  workloadImages,
  type CurrentAnswer,
} from './place-images.ts'

const WEB = 'registry.f220.ru/test/versions-web@sha256:6e39f98f9eaf01cb028aaedf92d639f540207e488119cc9db7847e9676f243ca'
const CONTENT = 'registry.f220.ru/test/versions-content@sha256:b4b538e4811594010277199a364aa85e0044b8f33951a91cdfe47abe885a88a2'

/** What the module answered for `deploy2/dogit-test` after the blue run had settled. */
const TWO_IMAGES_SETTLED: CurrentAnswer = {
  asked: 'the cluster',
  desired: 1,
  ready: 1,
  known: true,
  settled: true,
  workload: 'versions',
  image: `${WEB},${CONTENT}`,
  pods: [
    { image: CONTENT, pods: 1 },
    { image: WEB, pods: 1, tags: ['v-blue-nginx-1.31.6', 'v-seq-3-nginx-1.31.6-final'] },
  ],
}

test('A settled two-image workload is not retiring anything', () => {
  assert.equal(replacing(TWO_IMAGES_SETTLED), '')
  assert.equal(replacingPods(TWO_IMAGES_SETTLED), 0)
})

test('The images of a workload with two containers are two images', () => {
  assert.deepEqual(workloadImages(TWO_IMAGES_SETTLED), [WEB, CONTENT])
})

test('A rollout is only begun once every image of the workload has a pod', () => {
  assert.equal(comingOn(TWO_IMAGES_SETTLED), `${WEB},${CONTENT}`)

  // One container of the two turned over and the other is still on its previous image:
  // a pod part-way up, which is neither the old rollout nor the new one.
  const halfUp: CurrentAnswer = {
    ...TWO_IMAGES_SETTLED,
    settled: false,
    image: 'reg/new-web@sha256:aaa,reg/new-content@sha256:ddd',
    pods: [
      { image: CONTENT, pods: 1 },
      { image: 'reg/new-web@sha256:aaa', pods: 1 },
    ],
  }
  // One container of two up is a pod that has not finished starting, not a rollout.
  assert.equal(comingOn(halfUp), '')
  // And the container still on its previous image is genuinely on its way out.
  assert.equal(replacing(halfUp), CONTENT)
})

test('Every pod is counted, not only the one on the image that was named first', () => {
  assert.equal(wantedPods(TWO_IMAGES_SETTLED), 2)
})

test('Both containers tags are the place tags', () => {
  assert.deepEqual(wantedTags(TWO_IMAGES_SETTLED), ['v-blue-nginx-1.31.6', 'v-seq-3-nginx-1.31.6-final'])
})

test('A one-image workload mid-rollout is still taken off its old image', () => {
  const mid: CurrentAnswer = {
    known: true,
    image: 'reg/new@sha256:aaa',
    desired: 3,
    pods: [
      { image: 'reg/new@sha256:aaa', pods: 1 },
      { image: 'reg/old@sha256:bbb', pods: 2 },
    ],
  }
  assert.equal(replacing(mid), 'reg/old@sha256:bbb')
  assert.equal(replacingPods(mid), 2)
  assert.equal(comingOn(mid), 'reg/new@sha256:aaa')
})

test('A one-image workload that has settled is not retiring anything', () => {
  const done: CurrentAnswer = {
    known: true,
    image: 'reg/new@sha256:aaa',
    desired: 3,
    pods: [{ image: 'reg/new@sha256:aaa', pods: 3 }],
  }
  assert.equal(replacing(done), '')
  assert.equal(replacingPods(done), 0)
})

test('A workload that names no image has nothing said about it either way', () => {
  const mute: CurrentAnswer = {
    known: true,
    desired: 1,
    pods: [{ image: 'reg/whatever@sha256:ccc', pods: 1 }],
  }
  // Nothing to compare against, so every pod looks like an image from somewhere else.
  // Saying so would be inventing an answer rather than reporting one.
  assert.equal(replacing(mute), '')
  assert.equal(servedBy(mute), 'reg/whatever@sha256:ccc')
})

test('A place the module cannot answer about says nothing at all', () => {
  const unknown: CurrentAnswer = { known: false }
  assert.deepEqual(podsOn(unknown), [])
  assert.equal(servedBy(unknown), '')
  assert.equal(comingOn(unknown), '')
  assert.equal(primaryImage(unknown), '')
  assert.equal(replacing(unknown), '')
  assert.equal(replacingPods(unknown), 0)
  assert.deepEqual(workloadImages(undefined), [])
  assert.equal(replacing(null), '')
})

test('A module that reports no per-image figures still names what runs', () => {
  const bare: CurrentAnswer = { known: true, image: `${WEB},${CONTENT}`, desired: 2 }
  assert.deepEqual(podsOn(bare), [
    { image: WEB, pods: 2 },
    { image: CONTENT, pods: 2 },
  ])
  assert.equal(replacing(bare), '')
  assert.equal(wantedPods(bare), 4)
})
