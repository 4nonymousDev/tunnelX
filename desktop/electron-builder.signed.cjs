// Used only by package:signed. Never claim a release is signed when signing
// credentials or the expected certificate subject are absent.
const publisher = process.env.TUNNELX_PUBLISHER_NAME?.trim()
if (!publisher || !process.env.CSC_LINK || !process.env.CSC_KEY_PASSWORD) {
  throw new Error('Signed release requires TUNNELX_PUBLISHER_NAME, CSC_LINK and CSC_KEY_PASSWORD.')
}
module.exports = {
  extends: null,
  ...require('./package.json').build,
  forceCodeSigning: true,
  win: { ...require('./package.json').build.win, publisherName: [publisher], verifyUpdateCodeSignature: true },
}
