# kwokdriver

This is an attempt at a way to preload OpenShift CRDs into a KWOK container and be able to spin them up on-demand from within a test suite. The idea is similar in concept to EnvTest, with the exception that a single client can drive multiple tests in parallel with each test isolated into separate containers on separate networks.

Right now, this doesn't do much because there is still a _lot_ of work to be done on it. But in general, the idea works like this:

1. Build your image using `kwokdriver build --kwok-image <KWOK image pullspec> --release-image <OCP release image pullspec> --final-image <desired KWOK cluster image pullspec>`. The build process will extract the CRDs from the OpenShift release payload image and install them within the KWOK cluster image that it produces, along with a few other objects that are needed for the MCO to work. Many of these objects are hard-coded to the specifics of a given OpenShift release payload.
2. Start the kwokdriver server `kwokdriver start --socket /tmp/kwokdriver.sock --kwok-cluster-image <desired KWOK cluster image pullspec>`. The provided KWOK cluster image pullspec is the default that the server will use. One can pass a different pullspec in from the API client for the server to use as well.
3. Use the provided client library to request a set of test containers for each test. Each request waits for the KWOK server itself to be ready before continuing.
4. When the test is complete, call the Destroy() method with the environment ID returned by the request to stop all of the containers for that particular test and remove the network they were connected to.

Each request will create its own isolated network and start the following containers attached to that network:

1. KWOK control plane (the image that is built in the first step).
2. A Machine Config Operator container (image pullspec is retrieved from the OCP release image).
3. A Machine Config Controller container (pullspec same as above).

When the kwokdriver binary terminates, all of the containers will be stopped and removed along with their networks. Additionally, at startup, it will stop any orphaned containers and remove any orphaned networks before continuing.
